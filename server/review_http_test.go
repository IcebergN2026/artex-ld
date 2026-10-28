package server

// ═══ ARTEX 二开 · [审核流 PR3/PR5/§2.9.6 HTTP 集成测试] ═══
// 覆盖人工审核 HTTP 面：approve（pending_executor 转正 + expected_version CAS）、
// reject（退回修订三件套）、待审列表 DTO（content_version）、关闭审核守卫（409+数量）。
// 需要 ARTEX_PG_DSN（无则 skip）。
// 二开日期：2026-10-27

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// setupReviewHTTPServer 建一个带真实 PG 的最小 Server（Manager+Engine 零值可用：
// emitActivity 只经 t.Store 落库、bumpDrop 用零值 sync.Map），并造一个开启了
// 审核的任务挂进 Manager。返回 (server, handler, token, task, cleanup 由 t.Cleanup 注册)。
func setupReviewHTTPServer(t *testing.T) (*Server, http.Handler, string, *Task) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	dbTask, err := d.CreateTaskWithOptions("review http test", "review http test goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	taskID := fmt.Sprintf("%d", dbTask.ID)
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM exploration_nodes WHERE exploration_id = $1`, dbTask.ExplorationID)
		_, _ = d.Exec(`DELETE FROM exploration_edges WHERE exploration_id = $1`, dbTask.ExplorationID)
		_, _ = d.Exec(`DELETE FROM exploration_anchors WHERE exploration_id = $1`, dbTask.ExplorationID)
		_, _ = d.Exec(`DELETE FROM activity WHERE exploration_id = $1`, dbTask.ExplorationID)
		_, _ = d.Exec(`DELETE FROM tasks WHERE id = $1`, dbTask.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id = $1`, dbTask.ExplorationID)
	})

	task := &Task{
		ID:            taskID,
		ExpID:         dbTask.ExplorationID,
		ReviewEnabled: true,
		Store:         d.Exploration(dbTask.ExplorationID),
		notify:        make(chan struct{}, 8),
	}
	m := &Manager{pg: d, tasks: map[string]*Task{taskID: task}}
	s := &Server{m: m, engine: &Engine{bc: NewBroadcaster()}, jwtKey: []byte("review-http-test-signing-key")}
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), token, task
}

// mustAddHTTPReviewIntent 造一条待审意图（proposed + 双闸门 + 可选 pending_executor）。
func mustAddHTTPReviewIntent(t *testing.T, task *Task, summary, pendingExec string) int64 {
	t.Helper()
	payload := map[string]any{"summary": summary, "executor": "review", "review_status": "pending"}
	if pendingExec != "" {
		payload["pending_executor"] = pendingExec
	}
	id, err := task.Store.AddNode("intent", payload, 5, "proposed", "planner", nil)
	if err != nil {
		t.Fatalf("add pending review intent: %v", err)
	}
	return id
}

func doAuthed(t *testing.T, h http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReviewApproveHTTPFlow(t *testing.T) {
	_, h, token, task := setupReviewHTTPServer(t)
	intentID := mustAddHTTPReviewIntent(t, task, "http 批准流测试", "worker")

	// 待审列表 DTO 必须带 content_version（前端拿它做 expected_version CAS）。
	rec := doAuthed(t, h, token, http.MethodGet, fmt.Sprintf("/api/tasks/%s/review/proposed", task.ID), "")
	if rec.Code != 200 {
		t.Fatalf("list proposed: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Intents []struct {
			ID             int64  `json:"id"`
			ContentVersion int    `json:"content_version"`
			PendingExec    string `json:"pending_executor"`
		} `json:"intents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Intents) != 1 {
		t.Fatalf("list parse: %v body=%s", err, rec.Body.String())
	}
	if list.Intents[0].ID != intentID || list.Intents[0].PendingExec != "worker" {
		t.Fatalf("list row mismatch: %+v", list.Intents[0])
	}
	ver := list.Intents[0].ContentVersion

	// 携带正确 expected_version 批准 → 200，且 pending_executor 转正、review_status 删除。
	rec = doAuthed(t, h, token, http.MethodPost,
		fmt.Sprintf("/api/tasks/%s/review/%d/approve", task.ID, intentID),
		fmt.Sprintf(`{"expected_version":%d}`, ver))
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	n, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	if n.State != "open" || p["executor"] != "worker" {
		t.Fatalf("approved node: state=%q executor=%v", n.State, p["executor"])
	}
	if _, has := p["review_status"]; has {
		t.Fatalf("review_status must be deleted on approve, got %v", p["review_status"])
	}

	// 重复批准（同一版本，节点已 open）→ 409 并发冲突。
	rec = doAuthed(t, h, token, http.MethodPost,
		fmt.Sprintf("/api/tasks/%s/review/%d/approve", task.ID, intentID),
		fmt.Sprintf(`{"expected_version":%d}`, ver))
	if rec.Code != 409 {
		t.Fatalf("re-approve must 409, got %d", rec.Code)
	}
}

func TestReviewDecisionRejectsMalformedJSON(t *testing.T) {
	_, h, token, task := setupReviewHTTPServer(t)
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "approve", path: "approve", body: `{"expected_version":`},
		{name: "reject", path: "reject", body: `{"reason":"valid reason","expected_version":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intentID := mustAddHTTPReviewIntent(t, task, "malformed JSON "+tt.name, "")
			path := fmt.Sprintf("/api/tasks/%s/review/%d/%s", task.ID, intentID, tt.path)
			if rec := doAuthed(t, h, token, http.MethodPost, path, tt.body); rec.Code != 400 {
				t.Fatalf("malformed JSON must 400, got %d %s", rec.Code, rec.Body.String())
			}
			n, err := task.Store.GetNode(intentID)
			if err != nil {
				t.Fatal(err)
			}
			if n.State != "proposed" {
				t.Fatalf("malformed request changed intent state to %q", n.State)
			}
		})
	}
}

func TestReviewApproveAllowsEmptyBodyCompatibility(t *testing.T) {
	_, h, token, task := setupReviewHTTPServer(t)
	intentID := mustAddHTTPReviewIntent(t, task, "empty body compatibility", "")
	path := fmt.Sprintf("/api/tasks/%s/review/%d/approve", task.ID, intentID)
	if rec := doAuthed(t, h, token, http.MethodPost, path, ""); rec.Code != 200 {
		t.Fatalf("empty body must retain legacy behavior, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestReviewRejectHTTPFlow(t *testing.T) {
	_, h, token, task := setupReviewHTTPServer(t)
	intentID := mustAddHTTPReviewIntent(t, task, "http 驳回流测试", "")
	base := fmt.Sprintf("/api/tasks/%s/review/%d/reject", task.ID, intentID)

	// reason 必填：空理由 → 400。
	if rec := doAuthed(t, h, token, http.MethodPost, base, `{"reason":"  "}`); rec.Code != 400 {
		t.Fatalf("empty reason must 400, got %d", rec.Code)
	}
	// 版本 CAS：陈旧 expected_version → 409。
	if rec := doAuthed(t, h, token, http.MethodPost, base, `{"reason":"r","expected_version":9999}`); rec.Code != 409 {
		t.Fatalf("stale version must 409, got %d %s", rec.Code, rec.Body.String())
	}
	// 合法驳回 → 200：proposed 保持、review_status=rejected、content_version+1、revision_requested 入队。
	n0, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	rec := doAuthed(t, h, token, http.MethodPost, base,
		fmt.Sprintf(`{"reason":"信息不足，补充端口扫描结果","expected_version":%d}`, n0.ContentVersion))
	if rec.Code != 200 {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	n1, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal(n1.Payload, &p)
	if n1.State != "proposed" || p["review_status"] != "rejected" {
		t.Fatalf("rejected node: state=%q review_status=%v", n1.State, p["review_status"])
	}
	if n1.ContentVersion != n0.ContentVersion+1 {
		t.Fatalf("reject must bump content_version: %d → %d", n0.ContentVersion, n1.ContentVersion)
	}
	// revision_requested 事件入队（§4.2.3 三件套：通知 planner 修订）。
	evs := task.drainTriggers()
	found := false
	for _, ev := range evs {
		if ev.Kind == "revision_requested" && ev.IntentID == intentID {
			found = true
		}
	}
	if !found {
		t.Fatalf("revision_requested trigger missing: %+v", evs)
	}
}

func TestTaskCloseReviewGuard(t *testing.T) {
	_, h, token, task := setupReviewHTTPServer(t)
	patch := func(body string) *httptest.ResponseRecorder {
		return doAuthed(t, h, token, http.MethodPatch, fmt.Sprintf("/api/tasks/%s", task.ID), body)
	}

	// 有 pending 稿时关闭审核 → 409 + 数量。
	mustAddHTTPReviewIntent(t, task, "守卫测试-pending", "")
	if rec := patch(`{"review_enabled":false}`); rec.Code != 409 {
		t.Fatalf("close with pending must 409, got %d %s", rec.Code, rec.Body.String())
	}
	// 同样有 rejected 稿时仍 409（且文案含两类数量）。
	rejID := mustAddHTTPReviewIntent(t, task, "守卫测试-rejected", "")
	n, err := task.Store.GetNode(rejID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := task.Store.RequestRevisionAt(rejID, "驳回成 rejected", n.ContentVersion); err != nil || !ok {
		t.Fatalf("make rejected: ok=%v err=%v", ok, err)
	}
	rec := patch(`{"review_enabled":false}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "1") {
		t.Fatalf("close with pending+rejected must 409 with counts, got %d %s", rec.Code, rec.Body.String())
	}
	// 清空待审/待修订队列（软删两条）后允许关闭 → 200。
	for _, id := range []int64{rejID} {
		nn, _ := task.Store.GetNode(id)
		if _, err := task.Store.SoftDeleteIntentAt(id, "测试清理", nn.ContentVersion); err != nil {
			t.Fatalf("soft delete rejected: %v", err)
		}
	}
	// pending 那条已被上一步的列表留下：找出来软删。
	pend, _ := task.Store.ProposedIntents(100)
	for _, pn := range pend {
		if _, err := task.Store.SoftDeleteIntentAt(pn.ID, "测试清理", pn.ContentVersion); err != nil {
			t.Fatalf("soft delete pending: %v", err)
		}
	}
	if rec := patch(`{"review_enabled":false}`); rec.Code != 200 {
		t.Fatalf("close after cleanup must 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestReviewMessagesIncludeDetail(t *testing.T) {
	_, h, token, task := setupReviewHTTPServer(t)
	// 写入一条超过 summary 截断长度的 review agent 回复。
	fullDetail := strings.Repeat("完整回复：多行分析，不能只显示摘要。\n", 30)
	activityID, err := task.Store.AppendActivity(db.Activity{
		Worker:  "review",
		Kind:    "result",
		Summary: "简短摘要",
		Detail:  fullDetail,
	})
	if err != nil {
		t.Fatalf("append review activity: %v", err)
	}

	rec := doAuthed(t, h, token, http.MethodGet, fmt.Sprintf("/api/tasks/%s/review/messages", task.ID), "")
	if rec.Code != 200 {
		t.Fatalf("review messages must 200, got %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Messages []struct {
			ID        int64  `json:"id"`
			Worker    string `json:"worker"`
			Kind      string `json:"kind"`
			Summary   string `json:"summary"`
			Detail    string `json:"detail"`
			IsError   bool   `json:"is_error"`
			CreatedAt int64  `json:"created_at"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, m := range body.Messages {
		if m.ID != activityID {
			continue
		}
		if m.Worker != "review" {
			t.Fatalf("worker=%q, want review", m.Worker)
		}
		if m.Kind != "result" {
			t.Fatalf("kind=%q, want result", m.Kind)
		}
		if len(fullDetail) <= 200 {
			t.Fatalf("test detail must exceed 200 bytes, got %d", len(fullDetail))
		}
		if m.Detail != fullDetail {
			t.Fatalf("detail was truncated or changed: got %d bytes, want %d", len(m.Detail), len(fullDetail))
		}
		return
	}
	t.Fatalf("inserted review activity %d not returned", activityID)
}
