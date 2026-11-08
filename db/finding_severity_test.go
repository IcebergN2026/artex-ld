package db

import (
	"fmt"
	"testing"
	"time"
)

func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"critical", "critical", false},
		{" HIGH\t", "high", false},
		{"Medium", "medium", false},
		{" low ", "low", false},
		{"high\x00", "high", false},
		{"", "", true},
		{"   ", "", true},
		{"info", "", true},
		{"unknown", "", true},
	}
	for _, c := range cases {
		got, err := normalizeSeverity(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("normalizeSeverity(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeSeverity(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAddFindingAndSetFindingSeverityPolicy(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	marker := fmt.Sprintf("__test_severity_policy_%d__", time.Now().UnixNano())
	task, err := d.CreateTask("severity policy", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM findings WHERE vulnclass=$1`, marker)
		_ = d.DeleteTask(task.ID)
	}()
	nodeID, err := d.Exploration(task.ExplorationID).AddNode(KindFinding, map[string]any{"severity": "HIGH"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.AddFinding(task.ID, nodeID, marker, "write policy", " HIGH\t", "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatalf("AddFinding with uppercase severity: %v", err)
	}
	var severity string
	if err := d.QueryRow(`SELECT severity FROM findings WHERE id=$1`, id).Scan(&severity); err != nil {
		t.Fatal(err)
	}
	if severity != "high" {
		t.Fatalf("AddFinding stored severity %q, want high", severity)
	}

	if n, err := d.SetFindingSeverity(id, " Medium "); err != nil || n != 1 {
		t.Fatalf("SetFindingSeverity canonical write: n=%d err=%v", n, err)
	}
	if err := d.QueryRow(`SELECT severity FROM findings WHERE id=$1`, id).Scan(&severity); err != nil {
		t.Fatal(err)
	}
	if severity != "medium" {
		t.Fatalf("SetFindingSeverity stored severity %q, want medium", severity)
	}
	var payloadSeverity string
	if err := d.QueryRow(`SELECT payload->>'severity' FROM exploration_nodes WHERE id=$1`, nodeID).Scan(&payloadSeverity); err != nil {
		t.Fatal(err)
	}
	if payloadSeverity != "medium" {
		t.Fatalf("node payload severity %q, want medium", payloadSeverity)
	}

	// Invalid updates must fail explicitly and leave both the row and its node
	// payload untouched.
	if n, err := d.SetFindingSeverity(id, "info"); err == nil || n != 0 {
		t.Fatalf("SetFindingSeverity(info): n=%d err=%v, want 0,error", n, err)
	}
	if err := d.QueryRow(`SELECT severity FROM findings WHERE id=$1`, id).Scan(&severity); err != nil {
		t.Fatal(err)
	}
	if severity != "medium" {
		t.Fatalf("invalid update changed severity to %q", severity)
	}
	if err := d.QueryRow(`SELECT payload->>'severity' FROM exploration_nodes WHERE id=$1`, nodeID).Scan(&payloadSeverity); err != nil {
		t.Fatal(err)
	}
	if payloadSeverity != "medium" {
		t.Fatalf("invalid update changed node payload to %q", payloadSeverity)
	}

	// Invalid new rows must be rejected before the INSERT.
	before := 0
	if err := d.QueryRow(`SELECT count(*) FROM findings WHERE vulnclass=$1`, marker).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "info", "unknown", " critical!! "} {
		id, err := d.AddFinding(task.ID, 0, marker, "invalid", bad, "summary", "", "worker", nil)
		if err == nil {
			t.Fatalf("AddFinding(%q) succeeded with id %d, want error", bad, id)
		}
		if id != 0 {
			t.Fatalf("AddFinding(%q) returned id %d on error", bad, id)
		}
	}
	after := 0
	if err := d.QueryRow(`SELECT count(*) FROM findings WHERE vulnclass=$1`, marker).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("invalid AddFinding left %d new rows, want %d", after-before, 0)
	}
}

func TestListFindingsForExportSortsHistoricalSeverity(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	marker := fmt.Sprintf("__test_severity_export_%d__", time.Now().UnixNano())
	defer func() { _, _ = d.Exec(`DELETE FROM findings WHERE vulnclass=$1`, marker) }()

	ids := make([]int64, 0, 5)
	for _, sev := range []string{"critical", "high", "medium", "low", "critical"} {
		id, err := d.AddFinding(0, 0, marker, sev, sev, "summary", "", "worker", nil)
		if err != nil {
			t.Fatalf("AddFinding(%q): %v", sev, err)
		}
		ids = append(ids, id)
	}
	// Simulate historical rows written before normalization: mixed case and an
	// unknown level. Readers must sort by the canonical value and keep unknown last.
	for id, value := range map[int]string{
		0: "CRITICAL",
		1: " \tHigh\r\n",
		2: "MEDIUM",
		3: "low",
		4: "unexpected",
	} {
		if _, err := d.Exec(`UPDATE findings SET severity=$1 WHERE id=$2`, value, ids[id]); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := d.ListFindingsForExport(FindingFilter{VulnClass: marker}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(ids) {
		t.Fatalf("export rows = %d, want %d", len(rows), len(ids))
	}
	var gotSeverity []string
	for _, row := range rows {
		gotSeverity = append(gotSeverity, row.Severity)
	}
	wantSeverity := []string{"CRITICAL", " \tHigh\r\n", "MEDIUM", "low", "unexpected"}
	for i := range wantSeverity {
		if gotSeverity[i] != wantSeverity[i] {
			t.Fatalf("export order = %v, want %v", gotSeverity, wantSeverity)
		}
	}
}

func TestRecordFindingSeverityPolicy(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	marker := fmt.Sprintf("__test_severity_record_%d__", time.Now().UnixNano())
	task, err := d.CreateTask("record severity policy", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM notification_events WHERE finding_id IN (SELECT id FROM findings WHERE vulnclass=$1)`, marker)
		_, _ = d.Exec(`DELETE FROM findings WHERE vulnclass=$1`, marker)
		_ = d.DeleteTask(task.ID)
	}()

	es := d.Exploration(task.ExplorationID)
	f, err := es.RecordFinding(t.Context(), RecordFindingInput{
		TaskID: task.ID, ExplorationID: task.ExplorationID,
		VulnClass: marker, Name: "uppercase", Severity: " HIGH\x00 ", Summary: "summary",
	})
	if err != nil {
		t.Fatalf("RecordFinding with uppercase severity: %v", err)
	}

	var severity, payloadSeverity, notifySeverity string
	if err := d.QueryRow(`SELECT severity FROM findings WHERE id=$1`, f.FindingID).Scan(&severity); err != nil {
		t.Fatal(err)
	}
	if severity != "high" {
		t.Fatalf("findings severity = %q, want high", severity)
	}
	if err := d.QueryRow(`SELECT payload->>'severity' FROM exploration_nodes WHERE id=$1`, f.NodeID).Scan(&payloadSeverity); err != nil {
		t.Fatal(err)
	}
	if payloadSeverity != "high" {
		t.Fatalf("node payload severity = %q, want high", payloadSeverity)
	}
	if err := d.QueryRow(`SELECT snapshot->>'severity' FROM notification_events WHERE finding_id=$1 ORDER BY id DESC LIMIT 1`, f.FindingID).Scan(&notifySeverity); err != nil {
		t.Fatal(err)
	}
	if notifySeverity != "high" {
		t.Fatalf("notification severity = %q, want high", notifySeverity)
	}

	// Invalid severity is rejected before any node/finding/notification write.
	if _, err := es.RecordFinding(t.Context(), RecordFindingInput{
		TaskID: task.ID, ExplorationID: task.ExplorationID,
		VulnClass: marker, Name: "invalid", Severity: "info", Summary: "must fail",
	}); err == nil {
		t.Fatal("RecordFinding(info) succeeded, want error")
	}
	var findingsCount, nodesCount int
	if err := d.QueryRow(`SELECT count(*) FROM findings WHERE vulnclass=$1`, marker).Scan(&findingsCount); err != nil {
		t.Fatal(err)
	}
	if findingsCount != 1 {
		t.Fatalf("invalid RecordFinding left %d findings, want 1", findingsCount)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND payload->>'vulnclass'=$2`, task.ExplorationID, marker).Scan(&nodesCount); err != nil {
		t.Fatal(err)
	}
	if nodesCount != 1 {
		t.Fatalf("invalid RecordFinding left %d finding nodes, want 1", nodesCount)
	}
}
