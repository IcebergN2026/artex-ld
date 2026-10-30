package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
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

func TestVulnDispatcherConfigExportSupportsDualMode(t *testing.T) {
	type triggerRow struct {
		AgentKey       string `json:"agent_key"`
		FindingMessage string `json:"finding_message"`
	}
	found := false
	for _, line := range readExportLines(t, "../config-export/agent_triggers.jsonl") {
		var row triggerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row.AgentKey != "vuln_dispatcher" {
			continue
		}
		found = true
		for _, required := range []string{
			"execution_mode",
			"vulnclass ∈ {getshell,webshell,reverse_shell}",
			"vulnerability_only",
			"vulnclass ∈ {rce,file_upload,deserialization,privesc,获取目标权限}",
			"不要求立足点",
		} {
			if !strings.Contains(row.FindingMessage, required) {
				t.Errorf("dispatcher trigger missing %q", required)
			}
		}
	}
	if !found {
		t.Fatal("vuln_dispatcher trigger not found")
	}
}
