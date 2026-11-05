package db

// ═══ ARTEX 二开 · [审核流 PR2-PR5 PG 集成测试] ═══
// 覆盖审核流改造的 db 层新语义：review_status 子状态机、版本 CAS、rejected 退回修订闭环、
// update/cancel（软删）、产出锁。需要 ARTEX_PG_DSN（无则 skip）。
// 二开日期：2026-10-25

import (
	"errors"
	"strings"
	"testing"
	"encoding/json"
)

// setupReviewFlowExploration 建一个专用 exploration 并注册清理。
func setupReviewFlowExploration(t *testing.T) (*DB, *ExplorationStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	var expID int64
	if err := d.QueryRow(`INSERT INTO explorations(goal) VALUES ('review-flow integration test') RETURNING id`).Scan(&expID); err != nil {
		d.Close()
		t.Fatalf("create exploration: %v", err)
	}
	t.Cleanup(func() {
		d.Exec(`DELETE FROM exploration_nodes WHERE exploration_id = $1`, expID)
		d.Exec(`DELETE FROM exploration_edges WHERE exploration_id = $1`, expID)
		d.Exec(`DELETE FROM exploration_anchors WHERE exploration_id = $1`, expID)
		d.Exec(`DELETE FROM activity WHERE exploration_id = $1`, expID)
		d.Exec(`DELETE FROM explorations WHERE id = $1`, expID)
		d.Close()
	})
	return d, d.Exploration(expID)
}

// mustAddPendingReview 造一条标准待审意图（proposed + executor=review + review_status=pending）。
func mustAddPendingReview(t *testing.T, es *ExplorationStore, summary, pendingExec string) int64 {
	t.Helper()
	payload := map[string]any{"summary": summary, "executor": "review", "review_status": "pending"}
	if pendingExec != "" {
		payload["pending_executor"] = pendingExec
	}
	id, err := es.AddNode("intent", payload, 5, "proposed", "planner", nil)
	if err != nil {
		t.Fatalf("add pending review intent: %v", err)
	}
	return id
}

func TestReviewFlowDBSemantics(t *testing.T) {
	d, es := setupReviewFlowExploration(t)

	t.Run("ApprovePendingExecutorPromotion", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "批准转正测试", "worker")
		ok, err := es.ApproveIntentAt(id, 0)
		if err != nil || !ok {
			t.Fatalf("ApproveIntentAt: ok=%v err=%v", ok, err)
		}
		n, _ := es.GetNode(id)
		var p map[string]any
		mustUnmarshal(t, n.Payload, &p)
		if n.State != "open" || p["executor"] != "worker" {
			t.Fatalf("expected open+executor=worker, got state=%s payload=%s", n.State, n.Payload)
		}
		if _, has := p["pending_executor"]; has {
			t.Fatalf("pending_executor should be deleted, payload=%s", n.Payload)
		}
		if _, has := p["review_status"]; has {
			t.Fatalf("review_status should be deleted, payload=%s", n.Payload)
		}
		if n.ContentVersion != 1 {
			t.Fatalf("content_version should bump to 1, got %d", n.ContentVersion)
		}
		// 重复批准 → 非待审
		if _, err := es.ApproveIntentAt(id, 1); !errors.Is(err, ErrNotPendingReview) {
			t.Fatalf("re-approve should be ErrNotPendingReview, got %v", err)
		}
	})

	t.Run("ApproveWithoutPendingExecutorDefaultsWorker", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "无预选执行器", "")
		if _, err := es.ApproveIntentAt(id, 0); err != nil {
			t.Fatal(err)
		}
		n, _ := es.GetNode(id)
		var p map[string]any
		mustUnmarshal(t, n.Payload, &p)
		if _, has := p["executor"]; has {
			t.Fatalf("executor key should be deleted (worker 默认不落键), payload=%s", n.Payload)
		}
	})

	t.Run("ApproveStaleVersion", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "版本冲突", "")
		if _, err := es.ApproveIntentAt(id, 99); !errors.Is(err, ErrStaleVersion) {
			t.Fatalf("expected ErrStaleVersion, got %v", err)
		}
	})

	t.Run("RequestRevisionCycle", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "驳回修订闭环", "")
		// 空理由 → ErrNoteRequired
		if _, _, _, err := es.RequestRevisionAt(id, "  ", 0); !errors.Is(err, ErrNoteRequired) {
			t.Fatalf("empty note should be ErrNoteRequired, got %v", err)
		}
		summary, newVer, ok, err := es.RequestRevisionAt(id, "范围外目标", 0)
		if err != nil || !ok {
			t.Fatalf("RequestRevisionAt: ok=%v err=%v", ok, err)
		}
		if summary != "驳回修订闭环" || newVer != 1 {
			t.Fatalf("summary=%q newVer=%d", summary, newVer)
		}
		n, _ := es.GetNode(id)
		var p map[string]any
		mustUnmarshal(t, n.Payload, &p)
		if n.State != "proposed" || p["review_status"] != "rejected" || p["executor"] != "review" {
			t.Fatalf("rejected draft should stay proposed+review, got state=%s payload=%s", n.State, n.Payload)
		}
		if intOf(p["revision_count"]) != 1 || p["review_note"] != "范围外目标" {
			t.Fatalf("revision meta wrong: %s", n.Payload)
		}
		// rejected 不再进待审闸门（注意：其他子测试在闸门里留有别的 pending 意图，只能按 id 断言）
		pend, err := es.ProposedIntents(50)
		if err != nil {
			t.Fatal(err)
		}
		for _, pn := range pend {
			if pn.ID == id {
				t.Fatalf("rejected intent #%d must not be in pending gate", id)
			}
		}
		// 但在 RejectedIntents 中可见
		rej, err := es.RejectedIntents(10)
		if err != nil || len(rej) != 1 || rej[0].ID != id {
			t.Fatalf("RejectedIntents should contain #%d: %v %v", id, rej, err)
		}
		if has, _ := es.HasRejectedIntent(); !has {
			t.Fatal("HasRejectedIntent should be true")
		}
		// 重复驳回 → ErrNotPendingReview
		if _, _, _, err := es.RequestRevisionAt(id, "再驳", 1); !errors.Is(err, ErrNotPendingReview) {
			t.Fatalf("re-reject should be ErrNotPendingReview, got %v", err)
		}
		// 版本陈旧 → ErrStaleVersion
		id2 := mustAddPendingReview(t, es, "驳回版本冲突", "")
		if _, _, _, err := es.RequestRevisionAt(id2, "理由", 42); !errors.Is(err, ErrStaleVersion) {
			t.Fatalf("expected ErrStaleVersion, got %v", err)
		}
	})

	t.Run("UpdateRejectedResubmits", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "改稿重送", "")
		if _, _, _, err := es.RequestRevisionAt(id, "改一下", 0); err != nil {
			t.Fatal(err)
		}
		sum := "改为站内目录枚举"
		pe := "worker"
		upd, err := es.UpdateIntent(id, 1, IntentPatch{Summary: &sum, TargetExecutor: &pe}, "planner")
		if err != nil {
			t.Fatalf("UpdateIntent on rejected: %v", err)
		}
		if upd.ReviewStatus != "pending" || upd.ContentVersion != 2 {
			t.Fatalf("resubmit should flip to pending v2, got %+v", upd)
		}
		n, _ := es.GetNode(id)
		var p map[string]any
		mustUnmarshal(t, n.Payload, &p)
		if p["review_note"] != nil && p["review_note"] != "" {
			t.Fatalf("review_note should be cleared on resubmit: %s", n.Payload)
		}
		if intOf(p["revision_count"]) != 1 {
			t.Fatalf("revision_count should be kept=1: %s", n.Payload)
		}
		if p["pending_executor"] != "worker" || p["summary"] != sum {
			t.Fatalf("patch not applied: %s", n.Payload)
		}
		// 重送后回到待审闸门
		if has, _ := es.HasProposedIntent(); !has {
			t.Fatal("resubmitted intent should be back in pending gate")
		}
	})

	t.Run("UpdatePendingRejected", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "pending 禁改", "")
		sum := "试图改 pending"
		if _, err := es.UpdateIntent(id, 0, IntentPatch{Summary: &sum}, "planner"); !errors.Is(err, ErrIntentNotEditable) {
			t.Fatalf("pending update should be ErrIntentNotEditable, got %v", err)
		}
	})

	t.Run("UpdateStaleVersion", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "open 改稿", "")
		if _, err := es.ApproveIntentAt(id, 0); err != nil {
			t.Fatal(err)
		}
		sum := "x"
		if _, err := es.UpdateIntent(id, 99, IntentPatch{Summary: &sum}, "planner"); !errors.Is(err, ErrStaleVersion) {
			t.Fatalf("expected ErrStaleVersion, got %v", err)
		}
	})

	t.Run("ProduceLock", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "产出锁源", "")
		if _, err := es.ApproveIntentAt(id, 0); err != nil {
			t.Fatal(err)
		}
		// 造一个 yields 出边：intent → fact
		factID, err := es.AddNode("fact", map[string]any{"summary": "子事实", "confidence": "observed"}, 0, "confirmed", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`INSERT INTO exploration_edges(exploration_id, src_id, dst_id, rel) VALUES ($1,$2,$3,'yields')`, es.expID, id, factID); err != nil {
			t.Fatal(err)
		}
		sum := "被产出锁住"
		if _, err := es.UpdateIntent(id, 1, IntentPatch{Summary: &sum}, "planner"); !errors.Is(err, ErrIntentProduced) {
			t.Fatalf("produced intent should be ErrIntentProduced, got %v", err)
		}
	})

	t.Run("SoftDeleteRejected", func(t *testing.T) {
		id := mustAddPendingReview(t, es, "放弃修改", "")
		if _, _, _, err := es.RequestRevisionAt(id, "不合适", 0); err != nil {
			t.Fatal(err)
		}
		if summary, err := es.SoftDeleteIntent(id, "人工取消"); err != nil || summary != "放弃修改" {
			t.Fatalf("SoftDeleteIntent: summary=%q err=%v", summary, err)
		}
		n, _ := es.GetNode(id)
		if n.State != "deleted" {
			t.Fatalf("expected deleted, got %s", n.State)
		}
	})

	t.Run("FreshPendingIntents", func(t *testing.T) {
		fresh, err := es.FreshPendingIntents(50)
		if err != nil {
			t.Fatal(err)
		}
		// 所有仍 pending 的都应算 fresh（从未预审，reviewed_version 缺失）
		for _, n := range fresh {
			var p map[string]any
			mustUnmarshal(t, n.Payload, &p)
			if p["review_status"] != "pending" {
				t.Fatalf("fresh list must be pending-only: %s", n.Payload)
			}
		}
	})
}

// mustUnmarshal 测试辅助：解析 payload。
func mustUnmarshal(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal payload: %v (%s)", err, raw)
	}
}

var _ = strings.TrimSpace // 保留 import 供后续断言扩展
