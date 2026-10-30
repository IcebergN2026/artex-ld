package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestFindingTriggerContextIncludesFindingID(t *testing.T) {
	ctx := findingTriggerContext(db.TaskEvent{FindingID: 163, VulnClass: "getshell", Severity: "critical", Summary: "SSH 已验证"})
	if !strings.Contains(ctx, "finding_id: 163") || !strings.Contains(ctx, "getshell") {
		t.Fatalf("触发上下文缺少 Finding 身份或内容: %s", ctx)
	}
}

func TestVulnDispatcherHandoffAndGoalContract(t *testing.T) {
	p := agent.VulnDispatcherDefaultPrompt
	for _, want := range []string{
		"append_finding_to_task", "already_appended", "已有的对应内网渗透任务",
		"description：写完整的七段执行说明", "获取内网最高权限", "定位管理员、域控或其他关键管理主机并横向移动",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("Dispatcher 缺少规则 %q", want)
		}
	}
	if strings.Contains(p, "description：只写简短标题") || strings.Contains(p, "goal 是唯一完整执行说明") {
		t.Fatal("仍存在旧的 description/goal 契约")
	}
}

func TestAppendFindingToTaskSameAsset(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.pg.Exec(`DELETE FROM tasks WHERE id IN (SELECT task_id FROM task_dispatch_targets WHERE asset_key='ip:192.0.2.77')`); err != nil {
		t.Fatal(err)
	}
	parent, err := m.CreateTaskWithOptions("外网父任务", "验证目标", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parentID := mustTaskID(t, parent.ID)
	if _, err := m.pg.SeedTaskCategoryPresetIfEmpty("内网渗透", true, 0, 0, false, ""); err != nil {
		t.Fatal(err)
	}
	category, err := m.pg.GetTaskCategoryByNKey("内网渗透")
	if err != nil || category == nil {
		t.Fatalf("读取内网分类失败: %v", err)
	}
	assetID, err := m.pg.Assets().UpsertIP(db.UpsertIPReq{IP: "192.0.2.77", TaskID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	newFinding := func(summary string) (int64, int64) {
		nodeID, err := parent.Store.AddNode("finding", map[string]any{
			"summary": summary, "vulnclass": "getshell", "severity": "critical",
		}, 0, "confirmed", "worker", []int64{assetID})
		if err != nil {
			t.Fatal(err)
		}
		findingID, err := m.pg.AddFinding(parentID, nodeID, "getshell", "SSH 弱口令", "critical",
			summary, "paramiko 连接成功；id 返回 uid=1000", "worker", []int64{assetID})
		if err != nil {
			t.Fatal(err)
		}
		return findingID, nodeID
	}
	findingID, nodeID := newFinding("ubuntu/ubuntu 可登录")
	child, err := m.CreateTaskWithOptions("内网子任务", "深化内网", db.TaskCreateOptions{
		CategoryID: &category.ID, DispatchFindingID: findingID,
		DispatchAssetKeys: []string{"ip:192.0.2.77"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 不再要求直接父子血缘；资产登记才是追加依据。
	child.ParentRef = "999999"
	child.CategoryName = category.NKey
	child.Status = "running"

	events, err := m.pg.NewFindingsSince(nodeID - 1)
	if err != nil {
		t.Fatal(err)
	}
	foundEvent := false
	for _, event := range events {
		if event.NodeID == nodeID && event.FindingID == findingID {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("Finding 触发事件未携带全局 FindingID=%d", findingID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // admission changes state without starting background planner/worker work
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx}
	call := func(id int64) (string, error) {
		res, err := s.toolAppendFindingToTask().Call(context.Background(), json.RawMessage(
			fmt.Sprintf(`{"task_id":%q,"finding_id":%d}`, fmt.Sprint(child.ID), id)), nil)
		return res.Flatten(), err
	}
	first, err := call(findingID)
	if err != nil || !strings.Contains(first, `"status":"appended"`) {
		t.Fatalf("跨血缘同资产追加失败: %s err=%v", first, err)
	}
	second, err := call(findingID)
	if err != nil || !strings.Contains(second, `"status":"already_appended"`) {
		t.Fatalf("重复追加未幂等: %s err=%v", second, err)
	}
	var hintCount int
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_nodes
		WHERE exploration_id=$1 AND kind='hint' AND payload->>'text' LIKE $2`,
		child.ExpID, findingHandoffMarker(findingID)+"%").Scan(&hintCount); err != nil {
		t.Fatal(err)
	}
	if hintCount != 1 {
		t.Fatalf("Finding 应只生成一条 hint，实际 %d", hintCount)
	}
	var hintText string
	if err := m.pg.QueryRow(`SELECT payload->>'text' FROM exploration_nodes
		WHERE exploration_id=$1 AND kind='hint' AND payload->>'text' LIKE $2`,
		child.ExpID, findingHandoffMarker(findingID)+"%").Scan(&hintText); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Finding：#", "getshell", "ubuntu/ubuntu 可登录", "paramiko 连接成功"} {
		if !strings.Contains(hintText, want) {
			t.Errorf("追加内容缺少 %q: %s", want, hintText)
		}
	}
	var auditCount int
	if err := m.pg.QueryRow(`SELECT count(*) FROM activity
		WHERE exploration_id=$1 AND kind='finding_handoff'`, child.ExpID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("应只有一条追加审计，实际 %d", auditCount)
	}

	child.CategoryName = "外网渗透"
	if got, _ := call(findingID); !strings.Contains(got, "不是内网渗透") {
		t.Fatalf("未拒绝非内网任务: %s", got)
	}
	child.CategoryName = "内网渗透"

	// 已完成任务收到新的同资产 Finding：新增开放 Goal、写 hint 并复活。
	secondFindingID, _ := newFinding("同一资产的新 SSH 证据")
	if err := m.SetTaskStatus(child.ID, "done"); err != nil {
		t.Fatal(err)
	}
	third, err := call(secondFindingID)
	if err != nil || !strings.Contains(third, `"status":"appended"`) ||
		!strings.Contains(third, `"revived":true`) {
		t.Fatalf("终态任务未正确追加并复活: %s err=%v", third, err)
	}
	if child.Status != "running" {
		t.Fatalf("终态任务未恢复运行: %+v", child)
	}
	var goalCount int
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_nodes
		WHERE exploration_id=$1 AND kind='goal' AND state='open' AND payload->>'text' LIKE $2`,
		child.ExpID, findingHandoffMarker(secondFindingID)+"%").Scan(&goalCount); err != nil {
		t.Fatal(err)
	}
	if goalCount != 1 {
		t.Fatalf("终态任务应新增一个开放 Finding Goal，实际 %d", goalCount)
	}
}
