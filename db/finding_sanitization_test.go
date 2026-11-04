package db

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecordFindingSanitizesUntrustedText(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("finding binary evidence", "preserve finding with untrusted bytes", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.DeleteTask(task.ID) }()
	invalid := string([]byte{'b', 'a', 'd', 0xff, 0x00})
	finding, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{
		TaskID: task.ID, VulnClass: "xss\x00", Name: "binary finding " + invalid,
		Severity: "high\x00", Summary: "summary " + invalid, Evidence: "HTTP body " + invalid,
	})
	if err != nil {
		t.Fatalf("record finding with binary evidence: %v", err)
	}
	var name, summary, evidence string
	if err = d.QueryRow(`SELECT name,summary,evidence FROM findings WHERE id=$1`, finding.FindingID).Scan(&name, &summary, &evidence); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]string{"name": name, "summary": summary, "evidence": evidence} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			t.Fatalf("%s was not sanitized: %q", field, value)
		}
	}
	var payload []byte
	if err = d.QueryRow(`SELECT payload FROM exploration_nodes WHERE id=$1`, finding.NodeID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(payload) || !utf8.Valid(payload) || strings.ContainsRune(string(payload), '\x00') {
		t.Fatalf("finding node payload is not clean JSON: %q", payload)
	}
}
