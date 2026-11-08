package server

import (
	"strings"
	"testing"

	"artex/db"
)

func TestFindingTriggerContextIncludesExecutionMode(t *testing.T) {
	tests := []struct {
		name string
		goal string
		mode string
	}{
		{name: "default", goal: "验证 RCE", mode: "foothold"},
		{name: "vulnerability only", goal: "只验证漏洞，不需要getshell", mode: "vulnerability_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findingTriggerContext(db.TaskEvent{
				TaskID: 7, TaskGoal: tt.goal, VulnClass: "rce", Severity: "high", Summary: "confirmed",
			})
			if !strings.Contains(got, "执行模式: "+tt.mode) {
				t.Fatalf("finding context missing mode %q: %s", tt.mode, got)
			}
			if !strings.Contains(got, "[rce/high] confirmed") {
				t.Fatalf("finding context lost finding data: %s", got)
			}
		})
	}
}
