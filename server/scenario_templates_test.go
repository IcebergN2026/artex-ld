package server

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestSeedPromptsIncludesExternalPentestScenario(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer pg.Close()

	if _, err := pg.Exec(`DELETE FROM task_categories WHERE nkey='外网渗透'`); err != nil {
		t.Fatal(err)
	}
	seedPrompts(pg)
	seedPrompts(pg) // restart-safe: the second pass must not duplicate or overwrite.

	var review bool
	var plannerNKey, workerNKey string
	var count int
	if err := pg.QueryRow(`
SELECT c.default_review_enabled, pt.nkey, wt.nkey,
       (SELECT count(*) FROM task_categories WHERE nkey='外网渗透')
FROM task_categories c
JOIN planner_templates pt ON pt.id=c.default_planner_template_id
JOIN worker_templates wt ON wt.id=c.default_worker_template_id
WHERE c.nkey='外网渗透'`).Scan(&review, &plannerNKey, &workerNKey, &count); err != nil {
		t.Fatal(err)
	}
	if !review || plannerNKey != db.ExternalPentestTemplateName || workerNKey != db.ExternalPentestTemplateName || count != 1 {
		t.Fatalf("external scenario review=%v planner=%q worker=%q count=%d", review, plannerNKey, workerNKey, count)
	}
}
