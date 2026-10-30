package agent

import (
	"strings"
	"testing"
)

func TestInternalPlannerLowNoiseAndOptionalMCP(t *testing.T) {
	body := PlannerDomainSeeds()["内网渗透"]
	if body == "" {
		t.Fatal("内网渗透 Planner 模板不存在")
	}
	for _, unwanted := range []string{
		"whoami/hostname",
		"目标主机交互 shell",
		"指定网段覆盖",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("内网 Planner 仍包含固定判据或命令：%q", unwanted)
		}
	}
	for _, required := range []string{
		"按当前 Goal、意图和 Worker 带回的实测证据验收",
		"Planner 不指定 whoami、hostname 等固定命令",
		"选择低噪声、最少必要的操作",
		"WebShell、C2 和其他 MCP 都是可选方式",
		"没有可用 MCP 时，仍可规划其他可行的漏洞利用路径",
		"开局完成门槛",
		"必须先为当前任务派发信息收集意图",
		"开局强制前置",
		"不指定固定命令",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("内网 Planner 缺少规则：%q", required)
		}
	}
}
