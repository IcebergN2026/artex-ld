package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestRerunRecoverableIntents(t *testing.T) {
	type intentFixture struct {
		id      int64
		state   string
		reason  string
		summary string
	}

	seedIntent := func(t *testing.T, m *Manager, task *Task, state, executor, reason string) intentFixture {
		t.Helper()
		summary := state + " " + executor
		payload := map[string]any{"summary": summary}
		if executor != "" {
			payload["executor"] = executor
		}
		id, err := task.Store.AddIntent(payload, 1, nil, "planner")
		if err != nil {
			t.Fatalf("seed %s intent: %v", summary, err)
		}
		if _, err := m.pg.Exec(`UPDATE exploration_nodes SET state=$2, blocked_reason=NULLIF($3,'') WHERE id=$1`, id, state, reason); err != nil {
			t.Fatalf("set %s intent state: %v", summary, err)
		}
		return intentFixture{id: id, state: state, reason: reason, summary: summary}
	}

	seedIntents := func(t *testing.T, m *Manager, task *Task) (recoverable, controls []intentFixture) {
		t.Helper()
		for _, state := range []string{"blocked", "exhausted", "stopped"} {
			for _, executor := range []string{"", "rerun_recoverable_executor"} {
				reason := ""
				if state == "blocked" {
					reason = "retry-" + state + "-" + executor
				}
				recoverable = append(recoverable, seedIntent(t, m, task, state, executor, reason))
			}
		}
		for _, state := range []string{"done", "running"} {
			for _, executor := range []string{"", "rerun_recoverable_executor"} {
				controls = append(controls, seedIntent(t, m, task, state, executor, "keep-"+state+"-"+executor))
			}
		}
		return recoverable, controls
	}

	newHandler := func(t *testing.T, m *Manager) (http.Handler, string) {
		t.Helper()
		s := newAdmissionTestServer(m, nil)
		s.jwtKey = []byte("rerun-recoverable-route-test-key")
		token, err := signJWT(s.jwtKey)
		if err != nil {
			t.Fatalf("sign jwt: %v", err)
		}
		return s.Handler(), token
	}

	request := func(handler http.Handler, token, taskID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/intents/rerun-recoverable", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	t.Run("reopens mixed recoverable intents through real mux", func(t *testing.T) {
		m, err := NewManager(t.TempDir(), "")
		if err != nil {
			t.Skipf("postgres unavailable (%v) - skipping", err)
		}
		defer m.Close()
		task, err := m.CreateTask("rerun recoverable route", "reopen every recoverable intent", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
		recoverable, _ := seedIntents(t, m, task)
		handler, token := newHandler(t, m)

		recorder := request(handler, token, task.ID)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			ID       string `json:"id"`
			Reopened int64  `json:"reopened"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
		}
		if response.ID != task.ID || response.Reopened != int64(len(recoverable)) {
			t.Fatalf("response=%+v, want id=%s reopened=%d", response, task.ID, len(recoverable))
		}
		// Do not assert reopened node states here: successful admission may let a
		// worker claim them before this test can read them back.
	})

	t.Run("admission failure restores every original snapshot", func(t *testing.T) {
		m, err := NewManager(t.TempDir(), "")
		if err != nil {
			t.Skipf("postgres unavailable (%v) - skipping", err)
		}
		defer m.Close()
		task, err := m.CreateTask("failed recoverable rerun", "restore all intent snapshots", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		taskID, err := strconv.ParseInt(task.ID, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = m.pg.Exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, taskID)
			_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
		}()
		recoverable, controls := seedIntents(t, m, task)
		if err := m.SetTaskStatus(task.ID, "done"); err != nil {
			t.Fatal(err)
		}
		// The in-memory task remains routable, while ApplyTaskAdmission cannot
		// update its soft-deleted row. This deterministically fails after reopen.
		if _, err := m.pg.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, taskID); err != nil {
			t.Fatal(err)
		}
		handler, token := newHandler(t, m)

		recorder := request(handler, token, task.ID)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		assertRestored := func(fixtures []intentFixture) {
			t.Helper()
			for _, fixture := range fixtures {
				node, err := task.Store.GetNode(fixture.id)
				if err != nil {
					t.Fatalf("read %s intent: %v", fixture.summary, err)
				}
				if node == nil || node.State != fixture.state || node.BlockedReason != fixture.reason {
					t.Fatalf("%s intent after rollback=%+v, want state=%s blocked_reason=%q", fixture.summary, node, fixture.state, fixture.reason)
				}
			}
		}
		assertRestored(recoverable)
		assertRestored(controls)
	})
}
