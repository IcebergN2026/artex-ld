package agent

import (
	"strings"
	"testing"
)

func TestExecutionModeFromGoal(t *testing.T) {
	tests := []struct {
		name string
		goal string
		want ExecutionMode
	}{
		{name: "default", goal: "对目标进行渗透测试", want: ExecutionModeFoothold},
		{name: "generic validation is not opt in", goal: "验证 RCE", want: ExecutionModeFoothold},
		{name: "explicit shell", goal: "验证 RCE 并建立反弹 Shell", want: ExecutionModeFoothold},
		{name: "only validation", goal: "只验证漏洞，不需要 getshell", want: ExecutionModeVulnerabilityOnly},
		{name: "only reproduction", goal: "仅复现漏洞，不建立 Shell", want: ExecutionModeVulnerabilityOnly},
		{name: "structured marker", goal: "[执行模式:仅漏洞路径] 验证目标 RCE", want: ExecutionModeVulnerabilityOnly},
		{name: "english", goal: "Vulnerability validation only; no shell required", want: ExecutionModeVulnerabilityOnly},
		{name: "shell wins", goal: "只验证漏洞但需要getshell", want: ExecutionModeFoothold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExecutionModeFromGoal(tt.goal); got != tt.want {
				t.Fatalf("ExecutionModeFromGoal(%q)=%q want %q", tt.goal, got, tt.want)
			}
		})
	}
}

func TestExecutionModePromptSpecs(t *testing.T) {
	if got := plannerExecutionModeSpec("验证 RCE"); got != "" {
		t.Fatal("generic validation must not receive vulnerability-only Planner spec")
	}
	if got := plannerExecutionModeSpec("只验证漏洞，不需要getshell"); !strings.Contains(got, "不把建立 Shell") {
		t.Fatalf("vulnerability-only Planner spec missing: %q", got)
	}
	if got := goalExecutionModeSpec("仅复现漏洞", ""); !strings.Contains(got, "RCE、文件上传、反序列化") {
		t.Fatalf("vulnerability-only Goal spec missing: %q", got)
	}
	if got := goalExecutionModeSpec("获取交互 Shell", ""); got != "" {
		t.Fatal("foothold task must not receive vulnerability-only Goal spec")
	}
}

func TestVulnDispatcherDualModeAndGoalContract(t *testing.T) {
	prompt := VulnDispatcherDefaultPrompt
	for _, required := range []string{
		"execution_mode=vulnerability_only",
		"不要求 WebShell、C2 或交互会话",
		"description：写完整的七段执行说明",
		"goal：写任务的最终进攻/交付目标",
		"不默认生成单资产限制",
		"完成目标",
		"执行通道",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("dispatcher prompt missing %q", required)
		}
	}
}
