package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestExternalPentestDefaultMigration(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	for _, table := range []string{"planner_templates", "worker_templates"} {
		if _, err := d.Exec(`UPDATE ` + table + ` SET is_default=false WHERE is_default`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`DELETE FROM ` + table + ` WHERE builtin AND nkey IN ('内置默认','外网渗透')`); err != nil {
			t.Fatal(err)
		}
	}
	var plannerID, workerID int64
	if err := d.QueryRow(`INSERT INTO planner_templates(name,nkey,body,builtin,is_default) VALUES('内置默认','内置默认','planner body',true,true) RETURNING id`).Scan(&plannerID); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`INSERT INTO worker_templates(name,nkey,body,builtin,is_default) VALUES('内置默认','内置默认','worker body',true,true) RETURNING id`).Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM planner_templates WHERE id=$1`, plannerID)
		_, _ = d.Exec(`DELETE FROM worker_templates WHERE id=$1`, workerID)
	})

	conn, err := d.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := migrateExternalPentestDefaultTemplates(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		table string
		id    int64
		body  string
	}{{"planner_templates", plannerID, "planner body"}, {"worker_templates", workerID, "worker body"}} {
		var name, nkey, body string
		if err := d.QueryRow(`SELECT name,nkey,body FROM `+check.table+` WHERE id=$1`, check.id).Scan(&name, &nkey, &body); err != nil {
			t.Fatal(err)
		}
		if name != ExternalPentestTemplateName || nkey != ExternalPentestTemplateName || body != check.body {
			t.Fatalf("%s row=(%q,%q,%q)", check.table, name, nkey, body)
		}
	}
}

func TestBuiltinWorkerTemplateBodyEditable(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	name := fmt.Sprintf("Builtin editable %d", time.Now().UnixNano())
	tpl, err := d.CreateWorkerTemplate(WorkerTemplateInput{Name: name, Body: "before"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE worker_templates SET builtin=true WHERE id=$1`, tpl.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM worker_templates WHERE id=$1`, tpl.ID) })

	body := "after {{.WorkerName}}"
	updated, err := d.PatchWorkerTemplate(tpl.ID, WorkerTemplatePatch{Body: &body})
	if err != nil {
		t.Fatalf("builtin body patch: %v", err)
	}
	if updated.Body != body || updated.NKey != tpl.NKey || !updated.Builtin {
		t.Fatalf("unexpected builtin patch: %+v", updated)
	}
	renamed := name + " renamed"
	if _, err := d.PatchWorkerTemplate(tpl.ID, WorkerTemplatePatch{Name: &renamed}); !errors.Is(err, ErrWorkerTemplateBuiltin) {
		t.Fatalf("builtin rename error=%v", err)
	}
	if _, err := d.DeleteWorkerTemplate(tpl.ID); !errors.Is(err, ErrWorkerTemplateBuiltin) {
		t.Fatalf("builtin delete error=%v", err)
	}
}

func TestWorkerTemplateAndTaskSnapshot(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	name := fmt.Sprintf("Worker template %d", time.Now().UnixNano())
	tpl, err := d.CreateWorkerTemplate(WorkerTemplateInput{Name: name, Body: "worker {{.WorkerName}}"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`UPDATE worker_templates SET is_default=false WHERE id=$1`, tpl.ID)
		_, _ = d.DeleteWorkerTemplate(tpl.ID)
	})
	if _, err := d.CreateWorkerTemplate(WorkerTemplateInput{Name: name, Body: "duplicate"}); !errors.Is(err, ErrWorkerTemplateNameConflict) {
		t.Fatalf("duplicate error=%v", err)
	}
	if _, err := d.SetDefaultWorkerTemplate(tpl.ID); err != nil {
		t.Fatal(err)
	}
	body, err := d.DefaultWorkerTemplateBody()
	if err != nil || body != tpl.Body {
		t.Fatalf("default body=%q err=%v", body, err)
	}
	if _, err := d.DeleteWorkerTemplate(tpl.ID); !errors.Is(err, ErrWorkerTemplateDefaultProtected) {
		t.Fatalf("default delete error=%v", err)
	}

	category, err := d.CreateTaskCategory(name + " category")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteTaskCategory(category.ID) })
	if _, err := d.UpdateTaskCategoryPresets(category.ID, nil, nil, &tpl.ID, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	category, err = d.GetTaskCategory(category.ID)
	if err != nil || category.DefaultWorkerTemplateID != tpl.ID {
		t.Fatalf("category worker template=%d err=%v", category.DefaultWorkerTemplateID, err)
	}

	inserted, err := d.SeedTaskCategoryPresetIfEmpty(name+" seeded category", true, 0, tpl.ID, false, "")
	if err != nil || !inserted {
		t.Fatalf("seed category inserted=%v err=%v", inserted, err)
	}
	var seededCategoryID int64
	if err := d.QueryRow(`SELECT id FROM task_categories WHERE name=$1`, name+" seeded category").Scan(&seededCategoryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteTaskCategory(seededCategoryID) })
	seededCategory, err := d.GetTaskCategory(seededCategoryID)
	if err != nil {
		t.Fatal(err)
	}
	if seededCategory.DefaultWorkerTemplateID != tpl.ID {
		t.Fatalf("seeded worker template=%d, want %d", seededCategory.DefaultWorkerTemplateID, tpl.ID)
	}

	task, err := d.CreateTaskWithOptions(name+" task", "goal", TaskCreateOptions{CategoryID: &category.ID, WorkerPrompt: "snapshot {{.WorkerName}}"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	loaded, err := d.GetTask(task.ID)
	if err != nil || loaded.WorkerPromptOverride != "snapshot {{.WorkerName}}" {
		t.Fatalf("worker snapshot=%q err=%v", loaded.WorkerPromptOverride, err)
	}
}
