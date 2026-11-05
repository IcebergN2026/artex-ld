package agent

import (
	"strings"
	"testing"
)

func TestGoalAndExternalPlannerFootholdSemantics(t *testing.T) {
	goalRules := []string{
		"目标类型与 Shell 意图语义匹配",
		"强 Shell/立足点意图",
		"弱权限词不能单独判定为 getshell",
		"只有检测/验证意图时按实际漏洞分类",
		"WebShell、反弹 Shell、C2 是可选实现路径",
	}
	for _, rule := range goalRules {
		if !strings.Contains(goalsDefaultTmpl, rule) {
			t.Errorf("goals prompt missing rule %q", rule)
		}
	}

	plannerRules := []string{
		"Getshell 目标验收与证据语义匹配（仅 vulnclass=getshell）",
		"只有 webshell、reverse_shell 或 getshell 类型的确认 Finding",
		"攻击机本地 Shell、监听端口启动不算目标上线",
		"均不能单独证明 getshell",
		"它们可以完成相应的 rce、file_upload、deserialization、sqli 或权限验证目标",
		"调用 goal_met 前同样检查是否存在未完成的 getshell 目标",
	}
	for _, rule := range plannerRules {
		if !strings.Contains(plannerDefaultTmpl, rule) {
			t.Errorf("external planner prompt missing rule %q", rule)
		}
	}
}

func TestFootholdPromptChangeIsLimitedToExternalPlanner(t *testing.T) {
	const marker = "Getshell 目标验收与证据语义匹配"
	for name, body := range PlannerDomainSeeds() {
		if strings.Contains(body, marker) {
			t.Errorf("domain planner %q unexpectedly received external-only rule", name)
		}
	}
}
