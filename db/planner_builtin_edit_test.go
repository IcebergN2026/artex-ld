package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBuiltinPlannerTemplateBodyEditable(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	name := fmt.Sprintf("Builtin planner edit %d", time.Now().UnixNano())
	tpl, err := d.CreatePlannerTemplate(PlannerTemplateInput{Name: name, Body: "planner original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE planner_templates SET builtin=true WHERE id=$1`, tpl.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM planner_templates WHERE id=$1`, tpl.ID) })

	body := "planner edited"
	updated, err := d.PatchPlannerTemplate(tpl.ID, PlannerTemplatePatch{Body: &body})
	if err != nil {
		t.Fatalf("builtin body edit rejected: %v", err)
	}
	if updated.Body != "planner edited" || !updated.Builtin || updated.NKey != tpl.NKey || updated.Name != tpl.Name {
		t.Fatalf("unexpected builtin update: %+v", updated)
	}
	newName := name + " renamed"
	if _, err := d.PatchPlannerTemplate(tpl.ID, PlannerTemplatePatch{Name: &newName}); !errors.Is(err, ErrPlannerTemplateBuiltin) {
		t.Fatalf("builtin rename error=%v, want ErrPlannerTemplateBuiltin", err)
	}
	if _, err := d.DeletePlannerTemplate(tpl.ID); !errors.Is(err, ErrPlannerTemplateBuiltin) {
		t.Fatalf("builtin delete error=%v, want ErrPlannerTemplateBuiltin", err)
	}
}
