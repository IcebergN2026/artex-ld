package agent

import (
	"strings"
	"testing"
)

func TestScopePromptsAllowDiscoveredTargets(t *testing.T) {
	for _, forbidden := range []string{"超出该主机及直连可达范围的扩界", "授权边界：", "未明确授权", "需人工审批扩界"} {
		if strings.Contains(VulnDispatcherDefaultPrompt, forbidden) {
			t.Fatalf("dispatcher still contains retired scope rule %q", forbidden)
		}
	}
	if !strings.Contains(plannerDomainIntranetTmpl, "根据任务目标和已有证据继续规划") {
		t.Fatal("intranet planner must allow evidence-driven exploration")
	}
	worker := ""
	for _, seed := range WorkerDomainSeeds() {
		if seed.NKey == "内网渗透" {
			worker = seed.Body
		}
	}
	if !strings.Contains(worker, "继续探测和验证") {
		t.Fatal("intranet worker must allow testing discovered assets")
	}
}
