package agent

import (
	"strings"
	"testing"
)

func TestUnrestrictedPlannerAndWorkerPrompts(t *testing.T) {
	planner := plannerSystem("继续调查发现的资产", "/tmp/data", "/tmp/work", "")
	worker := workerSystem("", "", "/tmp/data", "/tmp/run", "worker-test", "")
	for name, body := range map[string]string{"planner": planner, "worker": worker} {
		for _, forbidden := range []string{"只在授权范围内操作", "授权边界", "操作约束（最高优先级", "需人工审批扩界"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s prompt still contains %q", name, forbidden)
			}
		}
		if !strings.Contains(body, "禁止破坏性操作") {
			t.Fatalf("%s prompt lost the destructive-operation guard", name)
		}
	}
}

func TestUnrestrictedIntranetTemplates(t *testing.T) {
	workerBody := ""
	for _, seed := range WorkerDomainSeeds() {
		if seed.NKey == "内网渗透" {
			workerBody = seed.Body
		}
	}
	for name, body := range map[string]string{
		"planner": PlannerDomainSeeds()["内网渗透"],
		"worker":  workerBody,
	} {
		for _, forbidden := range []string{"明确授权", "授权边界", "等待人工", "直连可达不等于"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s intranet template still contains %q", name, forbidden)
			}
		}
	}
}
