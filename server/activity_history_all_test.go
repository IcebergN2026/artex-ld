package server

// 任务轨迹(trajectory)tab 的 history 端点集成测试:
// session=all 时零值 ActivitySessionFilter 直落 store,返回整个任务所有 lane
// (mainagent / planner / 各 work#N)按 seq 升序合并的分页结果;session=bogus
// 仍 400(回归)。需要 ARTEX_PG_DSN(无则 skip)。
// 二开日期:2026-10-27

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"artex/db"
)

// mustAppendActivity 落一条 activity 步;worker/NodeID 由调用方给出。
func mustAppendActivity(t *testing.T, task *Task, a db.Activity) int64 {
	t.Helper()
	id, err := task.Store.AppendActivity(a)
	if err != nil {
		t.Fatalf("append activity (worker=%s kind=%s): %v", a.Worker, a.Kind, err)
	}
	return id
}

// getActivityHistory 请求 /api/exploration/activity/history 并解析响应。
func getActivityHistory(t *testing.T, s *Server, token, taskID, query string) (int, struct {
	Items          []ActivityDTO `json:"items"`
	SnapshotCursor int64         `json:"snapshot_cursor"`
	EarliestCursor int64         `json:"earliest_cursor"`
	HasMore        bool          `json:"has_more"`
}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/exploration/activity/history?task=%s&%s", taskID, query), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body struct {
		Items          []ActivityDTO `json:"items"`
		SnapshotCursor int64         `json:"snapshot_cursor"`
		EarliestCursor int64         `json:"earliest_cursor"`
		HasMore        bool          `json:"has_more"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("history body parse: %v body=%s", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestActivityHistoryAll(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	task, err := m.CreateTaskWithOptions("trajectory all test", "trajectory all test goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })

	// work#1 lane 需要挂在真实 intent 节点上(NodeID)。
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "trajectory test work"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	steps := []db.Activity{
		{Worker: "mainagent", Kind: "thinking", Summary: "main thinks"},
		{Worker: "planner", Kind: "text", Summary: "planner text"},
		{NodeID: &intentID, Worker: "work#1", Kind: "tool_use", Tool: "bash", Summary: "work runs bash"},
	}
	seqs := make([]int64, 0, len(steps))
	for _, a := range steps {
		seqs = append(seqs, mustAppendActivity(t, task, a))
	}

	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}

	// 最新一页:三个 lane 齐全、按 seq 升序。
	code, body := getActivityHistory(t, s, token, task.ID, "session=all")
	if code != http.StatusOK {
		t.Fatalf("session=all: status=%d", code)
	}
	if len(body.Items) != len(steps) {
		t.Fatalf("session=all items=%d want %d: %+v", len(body.Items), len(steps), body.Items)
	}
	workers := map[string]bool{}
	for i, it := range body.Items {
		if it.Seq != seqs[i] {
			t.Fatalf("item %d seq=%d want %d (ascending order)", i, it.Seq, seqs[i])
		}
		workers[it.Worker] = true
	}
	for _, w := range []string{"mainagent", "planner", "work#1"} {
		if !workers[w] {
			t.Fatalf("worker %q missing from merged timeline: %+v", w, body.Items)
		}
	}

	// 向上翻页:before=中间 seq 只返回更旧的一页。
	code, page := getActivityHistory(t, s, token, task.ID,
		fmt.Sprintf("session=all&before=%d", seqs[1]))
	if code != http.StatusOK {
		t.Fatalf("session=all paged: status=%d", code)
	}
	if len(page.Items) != 1 || page.Items[0].Seq != seqs[0] {
		t.Fatalf("older page mismatch: %+v want seq=%d", page.Items, seqs[0])
	}
	if page.HasMore {
		t.Fatalf("has_more must be false at oldest row: %+v", page)
	}
	if page.EarliestCursor != seqs[0] {
		t.Fatalf("earliest_cursor=%d want %d", page.EarliestCursor, seqs[0])
	}

	// 回归:bogus session 仍 400,既有会话键不受影响。
	for _, sess := range []string{"bogus", "main:-1", "intent:xyz"} {
		code, _ := getActivityHistory(t, s, token, task.ID, "session="+sess)
		if code != http.StatusBadRequest {
			t.Fatalf("session=%q must 400, got %d", sess, code)
		}
	}
	for _, sess := range []string{"main", "plan", fmt.Sprintf("intent:%d", intentID)} {
		code, _ := getActivityHistory(t, s, token, task.ID, "session="+sess)
		if code != http.StatusOK {
			t.Fatalf("session=%q must 200, got %d", sess, code)
		}
	}
}
