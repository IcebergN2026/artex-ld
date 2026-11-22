package db

import (
	"strings"
	"testing"
)

// TestNormalizeHeartbeatFloor pins the task-creation normalization contract:
// values below the 600s floor (including 0/300/599) are raised to 600, while
// 600 and larger values are preserved.
func TestNormalizeHeartbeatFloor(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-1, 600},
		{0, 600},
		{1, 600},
		{300, 600},
		{599, 600},
		{600, 600},
		{601, 601},
		{1200, 1200},
	}
	for _, tc := range cases {
		if got := normalizeHeartbeat(tc.in); got != tc.want {
			t.Errorf("normalizeHeartbeat(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestTaskHeartbeatDefaultEndToEnd verifies the PostgreSQL default itself, which
// also covers pre-existing databases because Open re-applies schema.sql (the
// idempotent ALTER COLUMN ... SET DEFAULT 600) on every startup.
func TestTaskHeartbeatDefaultEndToEnd(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	var columnDefault string
	if err := d.QueryRow(`
SELECT COALESCE(column_default, '')
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'tasks'
  AND column_name = 'plan_heartbeat_seconds'`).Scan(&columnDefault); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(columnDefault, "600") {
		t.Fatalf("tasks.plan_heartbeat_seconds default = %q, want 600", columnDefault)
	}

	// A raw insert that omits the column must materialize the schema default.
	var explorationID int64
	if err := d.QueryRow(`INSERT INTO explorations(goal) VALUES ('heartbeat default') RETURNING id`).Scan(&explorationID); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, explorationID) //nolint:errcheck

	var taskID int64
	if err := d.QueryRow(`
INSERT INTO tasks(description, goal, exploration_id)
VALUES ('heartbeat default', 'heartbeat default', $1)
RETURNING id`, explorationID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM tasks WHERE id=$1`, taskID) //nolint:errcheck

	var stored int
	if err := d.QueryRow(`SELECT plan_heartbeat_seconds FROM tasks WHERE id=$1`, taskID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 600 {
		t.Fatalf("DB-defaulted plan_heartbeat_seconds = %d, want 600", stored)
	}

	// CreateTaskWithOptions must normalize the same boundaries end to end.
	for _, tc := range []struct {
		in, want int
	}{
		{0, 600},
		{300, 600},
		{599, 600},
		{600, 600},
		{900, 900},
	} {
		tk, err := d.CreateTaskWithOptions("heartbeat normalization", "heartbeat normalization", TaskCreateOptions{
			PlanHeartbeatSeconds: tc.in,
		})
		if err != nil {
			t.Fatalf("create with heartbeat %d: %v", tc.in, err)
		}
		got, err := d.GetTask(tk.ID)
		if err != nil {
			_ = d.DeleteTask(tk.ID)
			t.Fatalf("get task with heartbeat %d: %v", tc.in, err)
		}
		if got == nil || got.PlanHeartbeatSeconds != tc.want {
			_ = d.DeleteTask(tk.ID)
			t.Fatalf("stored heartbeat for input %d = %+v, want %d", tc.in, got, tc.want)
		}
		if err := d.DeleteTask(tk.ID); err != nil {
			t.Fatalf("cleanup task with heartbeat %d: %v", tc.in, err)
		}
	}
}
