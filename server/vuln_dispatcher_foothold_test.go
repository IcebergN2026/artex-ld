package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func TestVulnDispatcherRequiresEstablishedFoothold(t *testing.T) {
	prompt := agent.VulnDispatcherDefaultPrompt
	for _, required := range []string{
		"vulnclass=webshell",
		"vulnclass ∈ {getshell, reverse_shell}",
		"WebShell 已上传成功，并已验证可访问或可连接",
		"C2/反弹会话已经上线",
		"仅证明 RCE、仅上传木马但未上线不算",
		"不要调用 spawn_task",
		"后续由新的 webshell/getshell/reverse_shell finding 再次触发",
		"只保留用户明确给出的",
		"不自行放松",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("dispatcher prompt missing %q", required)
		}
	}
	if strings.Contains(prompt, "vulnclass ∈ {getshell, rce, file_upload, deserialization}") {
		t.Fatal("dispatcher still treats RCE/file upload as a direct foothold")
	}
	if strings.Contains(prompt, "持久化后门") {
		t.Fatal("dispatcher still forbids persistence implants")
	}
}

func TestVulnDispatcherConfigExportMatchesFootholdGate(t *testing.T) {
	type promptRow struct {
		AgentKey     string `json:"agent_key"`
		TemplateText string `json:"template_text"`
	}
	promptFound := false
	for _, line := range readExportLines(t, "../config-export/agent_prompts_current.jsonl") {
		var row promptRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row.AgentKey == "vuln_dispatcher" {
			promptFound = true
			if row.TemplateText != agent.VulnDispatcherDefaultPrompt {
				t.Fatal("config-export dispatcher prompt differs from the built-in default")
			}
		}
	}
	if !promptFound {
		t.Fatal("config-export has no vuln_dispatcher prompt")
	}

	type triggerRow struct {
		AgentKey       string `json:"agent_key"`
		FindingMessage string `json:"finding_message"`
	}
	triggerFound := false
	for _, line := range readExportLines(t, "../config-export/agent_triggers.jsonl") {
		var row triggerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row.AgentKey == "vuln_dispatcher" {
			triggerFound = true
			if !strings.Contains(row.FindingMessage, "vulnclass ∈ {getshell,webshell,reverse_shell}") {
				t.Fatalf("dispatcher trigger does not include webshell: %s", row.FindingMessage)
			}
			if strings.Contains(row.FindingMessage, "{getshell,rce,deserialization,file_upload,reverse_shell,privesc}") {
				t.Fatal("dispatcher trigger still fires for pre-foothold RCE")
			}
		}
	}
	if !triggerFound {
		t.Fatal("config-export has no vuln_dispatcher trigger")
	}
}

func readExportLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// config-export uses PostgreSQL COPY text escaping, not raw JSONL escaping.
	text := strings.ReplaceAll(string(data), `\\`, `\`)
	return strings.Split(strings.TrimSpace(text), "\n")
}
