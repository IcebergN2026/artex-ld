package db

import (
	"fmt"
	"testing"
)

func TestLatestWorkerOutputSkipsLargeHistory(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("worker output", "latest result after long history")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
	store := d.Exploration(expID)
	intentID, err := store.AddIntent(map[string]any{"summary": "long worker"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AppendActivity(Activity{NodeID: &intentID, Kind: "text", Summary: "early text"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		if _, err = store.AppendActivity(Activity{NodeID: &intentID, Kind: "tool_result", Summary: fmt.Sprintf("step %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.AppendActivity(Activity{NodeID: &intentID, Kind: "result", Summary: "final summary", Detail: "final full output"}); err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestWorkerOutputWithSources(intentID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Kind != "result" || latest.Summary != "final summary" {
		t.Fatalf("latest worker output = %+v", latest)
	}
	detail, err := store.ActivityDetailWithSources(latest.ID)
	if err != nil || detail != "final full output" {
		t.Fatalf("latest detail = %q, %v", detail, err)
	}
}
