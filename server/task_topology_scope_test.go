package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
)

func TestTopologyRoutesRequireInternalCategoryNKey(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ensureCategory := func(nkey string) (*pgdb.TaskCategory, bool, error) {
		category, err := m.pg.GetTaskCategoryByNKey(nkey)
		if err != nil || category != nil {
			return category, false, err
		}
		category, err = m.pg.CreateTaskCategory(nkey)
		return category, true, err
	}
	external, externalCreated, err := ensureCategory("外网渗透")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if externalCreated {
			_, _ = m.pg.DeleteTaskCategory(external.ID)
		}
	})
	internal, internalCreated, err := ensureCategory("内网渗透")
	if err != nil {
		t.Fatal(err)
	}
	internalOriginalName := internal.Name
	t.Cleanup(func() {
		if internalCreated {
			_, _ = m.pg.Exec(`UPDATE tasks SET category_id=NULL WHERE category_id=$1`, internal.ID)
			_, _ = m.pg.DeleteTaskCategory(internal.ID)
		} else {
			_, _ = m.pg.RenameTaskCategory(internal.ID, internalOriginalName)
		}
	})
	stamp := time.Now().UnixNano()
	parent, err := m.CreateTaskWithOptions(fmt.Sprintf("external-%d", stamp), "external goal", pgdb.TaskCreateOptions{CategoryID: &external.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(parent.ID, DeleteTaskOptions{}) })
	child, err := m.CreateTaskWithOptions(fmt.Sprintf("internal-%d", stamp), "internal goal", pgdb.TaskCreateOptions{CategoryID: &internal.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(child.ID, DeleteTaskOptions{}) })
	child.ParentRef = parent.ID
	if parent.CategoryNKey != "外网渗透" || child.CategoryNKey != "内网渗透" {
		t.Fatalf("category nkeys: parent=%q child=%q", parent.CategoryNKey, child.CategoryNKey)
	}
	s := &Server{m: m}
	for _, tc := range []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{"read", http.MethodGet, s.taskTopology},
		{"relation", http.MethodPatch, s.updateTaskTopologyNode},
		{"note", http.MethodPost, s.addTaskTopologyNote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/tasks/"+parent.ID+"/pentest-topology", strings.NewReader(`{}`))
			req.SetPathValue("id", parent.ID)
			req.SetPathValue("ip", "192.0.2.1")
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "内网拓扑仅适用于内网渗透任务") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if _, err := m.pg.RenameTaskCategory(internal.ID, "内网显示名已修改"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+child.ID+"/pentest-topology", nil)
	req.SetPathValue("id", child.ID)
	rec := httptest.NewRecorder()
	s.taskTopology(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("internal child status=%d body=%s", rec.Code, rec.Body.String())
	}
	if dto := taskDTO(child, child.Status); dto.CategoryNKey != "内网渗透" {
		t.Fatalf("task DTO lost category nkey: %+v", dto)
	}
	if _, err := m.SetTaskCategory(child.ID, &external.ID); err != nil {
		t.Fatal(err)
	}
	if child.CategoryNKey != "外网渗透" {
		t.Fatalf("live category reassignment lost nkey: %q", child.CategoryNKey)
	}
	if _, err := m.SetTaskCategory(child.ID, &internal.ID); err != nil {
		t.Fatal(err)
	}
	if child.CategoryNKey != "内网渗透" {
		t.Fatalf("live category restore lost nkey: %q", child.CategoryNKey)
	}
}
