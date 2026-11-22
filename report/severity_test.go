package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"artex/db"
)

func TestSeverityRankCanonicalAndUnknownLast(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"critical", 0},
		{" HIGH ", 1},
		{"Medium", 2},
		{" low ", 3},
		{"info", 4},
		{"unknown", 4},
		{"", 4},
		{"   ", 4},
	}
	for _, c := range cases {
		if got := severityRank(c.in); got != c.want {
			t.Errorf("severityRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFindingsMarkdownSortsCanonicalSeverity(t *testing.T) {
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	fs := []*db.DBFinding{
		{ID: 1, Name: "unknown-severity", Severity: "info", CreatedAt: base},
		{ID: 2, Name: "empty-severity", Severity: "", CreatedAt: base},
		{ID: 3, Name: "low-row", Severity: "LOW", CreatedAt: base.Add(2 * time.Minute)},
		{ID: 4, Name: "high-row", Severity: " high ", CreatedAt: base.Add(3 * time.Minute)},
		{ID: 5, Name: "critical-row", Severity: "critical", CreatedAt: base.Add(4 * time.Minute)},
		{ID: 6, Name: "medium-row", Severity: "Medium", CreatedAt: base.Add(5 * time.Minute)},
	}
	out := FindingsMarkdown(fs, base)
	order := []string{"critical-row", "high-row", "medium-row", "low-row", "unknown-severity", "empty-severity"}
	last := -1
	for _, name := range order {
		pos := strings.Index(out, name)
		if pos < 0 {
			t.Fatalf("finding %q missing from export:\n%s", name, out)
		}
		if pos <= last {
			t.Fatalf("finding %q out of order in export:\n%s", name, out)
		}
		last = pos
	}

	// Historical uppercase values still count in their canonical summary bucket.
	for _, want := range []string{"| 严重 | 1 |", "| 高危 | 1 |", "| 中危 | 1 |", "| 低危 | 1 |"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestMarkdownSortsCanonicalSeverity(t *testing.T) {
	node := func(name, severity string) *db.Node {
		payload, err := json.Marshal(map[string]any{"name": name, "severity": severity})
		if err != nil {
			t.Fatal(err)
		}
		return &db.Node{Payload: payload}
	}
	out := Markdown(Input{
		Title: "severity ordering",
		Goal:  "each level once",
		Findings: []*db.Node{
			node("unknown-severity", "info"),
			node("empty-severity", ""),
			node("low-row", "LOW"),
			node("high-row", " high "),
			node("critical-row", "critical"),
			node("medium-row", "Medium"),
		},
	})
	order := []string{"critical-row", "high-row", "medium-row", "low-row", "unknown-severity", "empty-severity"}
	last := -1
	for _, name := range order {
		pos := strings.Index(out, name)
		if pos < 0 {
			t.Fatalf("finding %q missing from report:\n%s", name, out)
		}
		if pos <= last {
			t.Fatalf("finding %q out of order in report:\n%s", name, out)
		}
		last = pos
	}
}
