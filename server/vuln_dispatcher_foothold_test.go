package server

import (
	"strings"
	"testing"

	"artex/agent"
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
