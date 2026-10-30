package server

import (
	"testing"

	"github.com/Autumn-27/artex/agent"
	db "github.com/Autumn-27/artex/db"
)

func TestUpgradeScopeAuthorizationPrompts(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	oldSetting, hadSetting, err := pg.GetSetting(scopeAuthorizationPromptMigrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, scopeAuthorizationPromptMigrationKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadSetting {
			_ = pg.SetSetting(scopeAuthorizationPromptMigrationKey, oldSetting)
		} else {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, scopeAuthorizationPromptMigrationKey)
		}
	})

	planner, err := pg.GetPlannerTemplateByNKey("内网渗透")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := pg.GetWorkerTemplateByNKey("内网渗透")
	if err != nil {
		t.Fatal(err)
	}
	if planner == nil {
		if _, err := pg.SeedPlannerTemplateIfEmpty("内网渗透", "内网渗透", "legacy planner", false); err != nil {
			t.Fatal(err)
		}
	} else if _, err := pg.Exec(`UPDATE planner_templates SET body='legacy planner',builtin=true WHERE id=$1`, planner.ID); err != nil {
		t.Fatal(err)
	}
	if worker == nil {
		if _, err := pg.SeedWorkerTemplateIfEmpty("内网渗透", "内网渗透", "legacy worker", false); err != nil {
			t.Fatal(err)
		}
	} else if _, err := pg.Exec(`UPDATE worker_templates SET body='legacy worker',builtin=true WHERE id=$1`, worker.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if planner == nil {
			_, _ = pg.Exec(`DELETE FROM planner_templates WHERE nkey='内网渗透'`)
		} else {
			_, _ = pg.Exec(`UPDATE planner_templates SET body=$1,builtin=$2 WHERE id=$3`, planner.Body, planner.Builtin, planner.ID)
		}
		if worker == nil {
			_, _ = pg.Exec(`DELETE FROM worker_templates WHERE nkey='内网渗透'`)
		} else {
			_, _ = pg.Exec(`UPDATE worker_templates SET body=$1,builtin=$2 WHERE id=$3`, worker.Body, worker.Builtin, worker.ID)
		}
	})

	if err := upgradeScopeAuthorizationPrompts(pg); err != nil {
		t.Fatal(err)
	}
	planner, err = pg.GetPlannerTemplateByNKey("内网渗透")
	if err != nil || planner.Body != agent.PlannerDomainSeeds()["内网渗透"] {
		t.Fatalf("planner scope migration failed: err=%v", err)
	}
	worker, err = pg.GetWorkerTemplateByNKey("内网渗透")
	if err != nil {
		t.Fatal(err)
	}
	wantWorker := ""
	for _, seed := range agent.WorkerDomainSeeds() {
		if seed.NKey == "内网渗透" {
			wantWorker = seed.Body
		}
	}
	if worker.Body != wantWorker {
		t.Fatal("worker scope migration failed")
	}
	if _, err := pg.Exec(`UPDATE worker_templates SET body='local edit after migration' WHERE id=$1`, worker.ID); err != nil {
		t.Fatal(err)
	}
	if err := upgradeScopeAuthorizationPrompts(pg); err != nil {
		t.Fatal(err)
	}
	worker, _ = pg.GetWorkerTemplateByNKey("内网渗透")
	if worker.Body != "local edit after migration" {
		t.Fatal("migration must run only once and preserve later edits")
	}
}
