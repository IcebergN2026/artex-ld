package db

import (
	"testing"
	"time"
)

func invocationDB(t *testing.T) (*DB, int64) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	fid, err := d.AddFinding(0, 0, "invocation-test", "调用记录测试漏洞", "high", "original summary", "original evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM finding_agent_invocations WHERE finding_id=$1`, fid)
		_, _ = d.DeleteFinding(fid)
		d.Close()
	})
	return d, fid
}

func mustRecordInvocation(t *testing.T, d *DB, agentKey string, findingID, taskID int64, reason string) FindingInvocation {
	t.Helper()
	inv, inserted, err := d.RecordFindingInvocation(agentKey, findingID, taskID, reason)
	if err != nil || !inserted {
		t.Fatalf("record invocation %s/%d: inserted=%v err=%v", agentKey, findingID, inserted, err)
	}
	return inv
}

func TestFindingInvocationRecordIdempotent(t *testing.T) {
	d, fid := invocationDB(t)

	inv, inserted, err := d.RecordFindingInvocation("vuln_dispatcher", fid, 101, "首次派发")
	if err != nil || !inserted {
		t.Fatalf("first record: inserted=%v err=%v", inserted, err)
	}
	if inv.AgentKey != "vuln_dispatcher" || inv.FindingID != fid || inv.TaskID != 101 || inv.Reason != "首次派发" {
		t.Fatal("stored fields mismatch", inv)
	}
	if inv.ConversationID != nil || inv.LastRetryAt != nil || inv.CreatedAt.IsZero() {
		t.Fatal("new invocation must start pending without retry timestamps", inv)
	}

	again, inserted, err := d.RecordFindingInvocation("vuln_dispatcher", fid, 202, "重复派发")
	if err != nil || inserted {
		t.Fatalf("duplicate record must read the existing row: inserted=%v err=%v", inserted, err)
	}
	if again.TaskID != 101 || again.Reason != "首次派发" {
		t.Fatal("conflict must not overwrite task_id/reason", again)
	}
	if !again.CreatedAt.Equal(inv.CreatedAt) {
		t.Fatal("conflict must not touch created_at", again.CreatedAt, inv.CreatedAt)
	}

	// 不同 agent 可以各自记录对同一 finding 的调用。
	other, inserted, err := d.RecordFindingInvocation("trace_cleaner", fid, 101, "另一目标")
	if err != nil || !inserted {
		t.Fatalf("second agent on same finding: inserted=%v err=%v", inserted, err)
	}
	if other.FindingID != fid || other.AgentKey != "trace_cleaner" {
		t.Fatal("second agent record mismatch", other)
	}
}

func TestFindingInvocationSetConversationOnlyWhenNull(t *testing.T) {
	d, fid := invocationDB(t)
	mustRecordInvocation(t, d, "vuln_dispatcher", fid, 1, "派发")

	if err := d.SetFindingInvocationConversation("vuln_dispatcher", fid, 777); err != nil {
		t.Fatal(err)
	}
	got, inserted, err := d.RecordFindingInvocation("vuln_dispatcher", fid, 1, "再读")
	if err != nil || inserted {
		t.Fatal(err)
	}
	if got.ConversationID == nil || *got.ConversationID != 777 {
		t.Fatal("conversation backfill missing", got)
	}

	// 重复/合并投递不能覆盖第一个已知会话 ID。
	if err := d.SetFindingInvocationConversation("vuln_dispatcher", fid, 888); err != nil {
		t.Fatal(err)
	}
	got, _, err = d.RecordFindingInvocation("vuln_dispatcher", fid, 1, "三读")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID == nil || *got.ConversationID != 777 {
		t.Fatal("second backfill must not overwrite the first conversation", got)
	}
}

func TestFindingInvocationClaimThrottle(t *testing.T) {
	d, fid := invocationDB(t)
	mustRecordInvocation(t, d, "agent_a", fid, 1, "fresh")

	// NULL + 不足 10 分钟：不可认领（覆盖 0 分钟与 9 分钟）。
	if ok, err := d.ClaimFindingInvocationRetry("agent_a", fid, 10*time.Minute); err != nil || ok {
		t.Fatalf("fresh invocation must not be claimable: ok=%v err=%v", ok, err)
	}
	if _, err := d.Exec(`UPDATE finding_agent_invocations SET created_at=now() - interval '9 minutes'
		WHERE agent_key='agent_a' AND finding_id=$1`, fid); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ClaimFindingInvocationRetry("agent_a", fid, 10*time.Minute); err != nil || ok {
		t.Fatalf("9-minute-old invocation must not be claimable: ok=%v err=%v", ok, err)
	}

	// NULL + 超过 10 分钟：可认领；认领推进 last_retry_at 后立即再次认领失败。
	if _, err := d.Exec(`UPDATE finding_agent_invocations SET created_at=now() - interval '11 minutes'
		WHERE agent_key='agent_a' AND finding_id=$1`, fid); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ClaimFindingInvocationRetry("agent_a", fid, 10*time.Minute); err != nil || !ok {
		t.Fatalf("due invocation must be claimed: ok=%v err=%v", ok, err)
	}
	if ok, err := d.ClaimFindingInvocationRetry("agent_a", fid, 10*time.Minute); err != nil || ok {
		t.Fatalf("immediate re-claim must fail: ok=%v err=%v", ok, err)
	}

	// 已回填 conversation 的记录永不被认领，即使时间早已到期。
	mustRecordInvocation(t, d, "agent_b", fid, 1, "已回填")
	if err := d.SetFindingInvocationConversation("agent_b", fid, 555); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE finding_agent_invocations SET created_at=now() - interval '2 hours'
		WHERE agent_key='agent_b' AND finding_id=$1`, fid); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ClaimFindingInvocationRetry("agent_b", fid, 10*time.Minute); err != nil || ok {
		t.Fatalf("backfilled invocation must never be claimed: ok=%v err=%v", ok, err)
	}
}

func TestFindingInvocationPendingSelection(t *testing.T) {
	d, fid := invocationDB(t)
	old := mustRecordInvocation(t, d, "agent_old", fid, 1, "最旧")
	mid := mustRecordInvocation(t, d, "agent_mid", fid, 1, "中间")
	mustRecordInvocation(t, d, "agent_fresh", fid, 1, "最新")
	if _, err := d.Exec(`UPDATE finding_agent_invocations SET created_at=now() - interval '1 hour'
		WHERE agent_key='agent_old' AND finding_id=$1`, fid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE finding_agent_invocations SET created_at=now() - interval '20 minutes'
		WHERE agent_key='agent_mid' AND finding_id=$1`, fid); err != nil {
		t.Fatal(err)
	}

	// 15 分钟门控：fresh（0 分钟）排除，old/mid 按 created_at 升序返回。
	pending, err := d.PendingFindingInvocations(15*time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, inv := range pending {
		if inv.FindingID == fid {
			keys = append(keys, inv.AgentKey)
		}
	}
	if len(keys) != 2 || keys[0] != old.AgentKey || keys[1] != mid.AgentKey {
		t.Fatal("pending selection/order mismatch", keys)
	}

	// limit 生效。
	limited, err := d.PendingFindingInvocations(15*time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].AgentKey != old.AgentKey {
		t.Fatal("limit=1 must return only the oldest", limited)
	}

	// 回填 mid 后只剩 old；认领 old 推进 last_retry_at 后也不再到期（COALESCE）。
	if err := d.SetFindingInvocationConversation("agent_mid", fid, 666); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ClaimFindingInvocationRetry("agent_old", fid, 15*time.Minute); err != nil || !ok {
		t.Fatal(err)
	}
	pending, err = d.PendingFindingInvocations(15*time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range pending {
		if inv.FindingID == fid {
			t.Fatal("no invocation of this finding may stay pending", inv)
		}
	}
}
