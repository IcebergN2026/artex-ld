package db

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCanonicalAssetKeysCollapsePortsAndURLs(t *testing.T) {
	port := 2001
	assets := []*Asset{
		{Type: "ip", IP: "192.0.2.10"},
		{Type: "service", IP: "192.0.2.10", Port: &port, URL: "http://192.0.2.10:2001"},
		{Type: "endpoint", Domain: "192.0.2.10", URL: "http://192.0.2.10:2003/index.php"},
		{Type: "root_domain", Domain: "Example.COM."},
		{Type: "service", URL: "https://example.com:443/login"},
	}
	got := CanonicalAssetKeys(assets)
	want := []string{"domain:example.com", "ip:192.0.2.10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical keys = %v, want %v", got, want)
	}
}

func TestTaskDispatchTargetsDeduplicateAcrossParentsAndConcurrentCreates(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	var categoryID int64
	category, err := d.GetTaskCategoryByNKey("内网渗透")
	createdCategory := false
	if err != nil {
		t.Fatal(err)
	}
	if category == nil {
		if err := d.QueryRow(`INSERT INTO task_categories(name,nkey) VALUES ('内网渗透-派发测试','内网渗透') RETURNING id`).Scan(&categoryID); err != nil {
			t.Fatal(err)
		}
		createdCategory = true
	} else {
		categoryID = category.ID
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM task_dispatch_targets WHERE finding_id=30`)
		_, _ = d.Exec(`DELETE FROM tasks WHERE category_id=$1`, categoryID)
		if createdCategory {
			_, _ = d.Exec(`DELETE FROM task_categories WHERE id=$1`, categoryID)
		}
	})
	create := func(assetKey string) (*Task, error) {
		return d.CreateTaskWithOptions("child", "goal", TaskCreateOptions{
			CategoryID: &categoryID, DispatchAssetKeys: []string{assetKey}, DispatchFindingID: 30,
		})
	}
	assetPrefix := fmt.Sprintf("ip:2001:db8::%x", time.Now().UnixNano())
	firstKey, secondKey, concurrentKey := assetPrefix, assetPrefix+":1", assetPrefix+":2"
	first, err := create(firstKey)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := create(firstKey)
	if duplicate != nil || err == nil {
		t.Fatalf("same asset duplicate = %#v, %v; want rollback and conflict", duplicate, err)
	}
	var conflict *TaskDispatchConflictError
	if !errors.As(err, &conflict) || conflict.TaskID != first.ID {
		t.Fatalf("error = %T %v, want conflict with task %d", err, err, first.ID)
	}
	targets, err := d.ListTaskDispatchTargets([]string{firstKey})
	if err != nil || len(targets) != 1 || targets[0].TaskID != first.ID {
		t.Fatalf("targets = %#v, %v", targets, err)
	}
	if other, err := create(secondKey); err != nil || other == nil {
		t.Fatalf("different asset must be allowed: %#v, %v", other, err)
	}

	const contenders = 6
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := create(concurrentKey)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.As(err, &conflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent create error: %v", err)
		}
	}
	if successes != 1 || conflicts != contenders-1 {
		t.Fatalf("concurrent creates: successes=%d conflicts=%d, want 1/%d", successes, conflicts, contenders-1)
	}
	var stored int
	if err := d.QueryRow(`SELECT count(*) FROM task_dispatch_targets WHERE asset_key=$1`, concurrentKey).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored dispatch targets = %d, %v; want 1", stored, err)
	}
	fmt.Printf("dispatch dedupe: sequential and %d-way concurrency passed\n", contenders)
}
