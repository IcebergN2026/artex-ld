package server

import (
	"testing"

	"artex/db"
)

func TestExternalPentestReviewDefaultMigration(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer pg.Close()
	t.Cleanup(stashSettingFlag(t, pg, "external_pentest_review_default_off_v1"))

	category, err := pg.GetTaskCategoryByNKey("外网渗透")
	created := false
	if err != nil {
		t.Fatal(err)
	}
	if category == nil {
		if _, err := pg.SeedTaskCategoryPresetIfEmpty("外网渗透", true, 0, 0, false, ""); err != nil {
			t.Fatal(err)
		}
		created = true
		category, err = pg.GetTaskCategoryByNKey("外网渗透")
		if err != nil || category == nil {
			t.Fatalf("create external category: %v", err)
		}
	}
	originalReview := category.DefaultReviewEnabled
	t.Cleanup(func() {
		if created {
			_, _ = pg.DeleteTaskCategory(category.ID)
		} else {
			_, _ = pg.Exec(`UPDATE task_categories SET default_review_enabled=$2 WHERE id=$1`, category.ID, originalReview)
		}
	})
	otherCategory, err := pg.GetTaskCategoryByNKey("内网渗透")
	if err != nil {
		t.Fatal(err)
	}
	otherCreated := false
	if otherCategory == nil {
		if _, err := pg.SeedTaskCategoryPresetIfEmpty("内网渗透", true, 0, 0, false, ""); err != nil {
			t.Fatal(err)
		}
		otherCreated = true
		otherCategory, err = pg.GetTaskCategoryByNKey("内网渗透")
		if err != nil || otherCategory == nil {
			t.Fatalf("create internal category: %v", err)
		}
	}
	if otherCreated {
		t.Cleanup(func() { _, _ = pg.DeleteTaskCategory(otherCategory.ID) })
	}
	otherReview := otherCategory.DefaultReviewEnabled

	if _, err := pg.Exec(`UPDATE task_categories SET default_review_enabled=true WHERE id=$1`, category.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key='external_pentest_review_default_off_v1'`); err != nil {
		t.Fatal(err)
	}
	upgradeExternalPentestReviewDefault(pg)
	var got bool
	if err := pg.QueryRow(`SELECT default_review_enabled FROM task_categories WHERE id=$1`, category.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("old external default was not disabled")
	}
	var gotOther bool
	if err := pg.QueryRow(`SELECT default_review_enabled FROM task_categories WHERE nkey='内网渗透'`).Scan(&gotOther); err != nil {
		t.Fatal(err)
	}
	if gotOther != otherReview {
		t.Fatalf("internal category changed: before=%v after=%v", otherReview, gotOther)
	}

	// A deliberate later re-enable must survive subsequent calls.
	if _, err := pg.Exec(`UPDATE task_categories SET default_review_enabled=true WHERE id=$1`, category.ID); err != nil {
		t.Fatal(err)
	}
	upgradeExternalPentestReviewDefault(pg)
	if err := pg.QueryRow(`SELECT default_review_enabled FROM task_categories WHERE id=$1`, category.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("manual re-enable was overwritten by the completed migration")
	}
}
