package db

import (
	"fmt"
	"testing"
	"time"
)

func TestTaskCategoryNKeyStableAcrossRename(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	category, err := d.GetTaskCategoryByNKey("内网渗透")
	if err != nil {
		t.Fatal(err)
	}
	created := category == nil
	originalName := ""
	if category != nil {
		originalName = category.Name
	}
	if created {
		category, err = d.CreateTaskCategory("内网渗透")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if created {
			_, _ = d.Exec(`UPDATE tasks SET category_id=NULL WHERE category_id=$1`, category.ID)
			_, _ = d.DeleteTaskCategory(category.ID)
		} else {
			_, _ = d.RenameTaskCategory(category.ID, originalName)
		}
	})
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("nkey-task-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{CategoryID: &category.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	loaded, err := d.GetTask(task.ID)
	if err != nil || loaded.CategoryNKey != "内网渗透" {
		t.Fatalf("category nkey=%q err=%v", loaded.CategoryNKey, err)
	}
	if _, err := d.RenameTaskCategory(category.ID, "内网显示名已修改"); err != nil {
		t.Fatal(err)
	}
	loaded, err = d.GetTask(task.ID)
	if err != nil || loaded.CategoryName != "内网显示名已修改" || loaded.CategoryNKey != "内网渗透" {
		t.Fatalf("renamed category task=%+v err=%v", loaded, err)
	}
}
