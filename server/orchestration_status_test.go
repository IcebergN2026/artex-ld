package server

import (
	"context"
	"encoding/json"
	"testing"
)

// TestListTasksStatusMatchesResolvedTaskStatus 验证 list_tasks 工具直接输出
// resolvedTaskStatus 的结果，而不是只测 resolvedTaskStatus 本身：
//   - queued 任务必须显示 queued；
//   - 已启动且 LLM 链耗尽的 chain_exhausted 任务必须显示 running（与页面一致）。
func TestListTasksStatusMatchesResolvedStatusOutputs(t *testing.T) {
	m, s := fiFixture(t)

	queuedTask := fiCreateTask(t, m, "list_tasks 状态测试：排队任务")
	if err := m.EnqueueTask(queuedTask.ID, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DequeueTask(queuedTask.ID, true) })

	exhaustedTask := fiCreateTask(t, m, "list_tasks 状态测试：链耗尽任务")
	t.Cleanup(func() { s.engine.started.Delete(exhaustedTask.ID) })
	s.engine.started.Store(exhaustedTask.ID, true)
	exhaustedTask.updateLifecycle(func(state *taskLifecycleState) {
		state.Status = "running"
	})
	profileID := int64(11)
	exhaustedTask.setLLMState(&profileID, nil, []int64{profileID}, 1, "chain_exhausted", "quota exhausted")

	wantQueued := s.resolvedTaskStatus(queuedTask)
	if wantQueued != "queued" {
		t.Fatalf("resolvedTaskStatus(queued)=%q, want queued", wantQueued)
	}
	wantExhausted := s.resolvedTaskStatus(exhaustedTask)
	if wantExhausted != "running" {
		t.Fatalf("resolvedTaskStatus(started+chain_exhausted)=%q, want running", wantExhausted)
	}

	result, err := s.toolListTasks().Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("list_tasks 返回错误: %s", result.Flatten())
	}
	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.Flatten()), &rows); err != nil {
		t.Fatalf("解析 list_tasks 输出失败: %v; raw=%s", err, result.Flatten())
	}
	statusByID := make(map[string]string, len(rows))
	for _, row := range rows {
		statusByID[row.ID] = row.Status
	}

	if got := statusByID[queuedTask.ID]; got != wantQueued {
		t.Fatalf("list_tasks queued 状态=%q, resolvedTaskStatus=%q", got, wantQueued)
	}
	if got := statusByID[exhaustedTask.ID]; got != wantExhausted {
		t.Fatalf("list_tasks started+chain_exhausted 状态=%q, resolvedTaskStatus=%q", got, wantExhausted)
	}
}
