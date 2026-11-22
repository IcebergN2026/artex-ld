package intercept

import "testing"

// agentBaseName:worker lane 身份带意图序号后缀("worker · #3"),规则按基名匹配。
func TestAgentBaseName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"worker · #3", "worker"},
		{"work#3 · #43", "worker"}, // 内置 worker 池真实 lane 身份（engine.go work#N）
		{"work#3", "worker"},
		{"work#x", "work#x"}, // 非数字序号不归一
		{"workflow", "workflow"},
		{"recon_scout · #12", "recon_scout"},
		{"worker", "worker"},
		{"planner", "planner"},
		{"", ""},
		{"weird · #name", "weird"}, // 分隔符是字面 " · #",不校验序号是否为数字
	}
	for _, c := range cases {
		if got := agentBaseName(c.in); got != c.want {
			t.Errorf("agentBaseName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ruleAppliesToAgent 是 DB 规则 agents 列(适用 agent 范围)的判定核心:
// "all"/空列表覆盖全部,否则要求与 ctx 绑定的 agent 基名精确相等。
func TestRuleAppliesToAgent(t *testing.T) {
	cases := []struct {
		name      string
		agents    []string
		agentName string
		want      bool
	}{
		{"all 覆盖任意 agent", []string{"all"}, "worker", true},
		{"all 覆盖空身份", []string{"all"}, "", true},
		{"空列表向后兼容旧行", nil, "worker", true},
		{"空切片向后兼容旧行", []string{}, "planner", true},
		{"精确命中 worker", []string{"worker"}, "worker", true},
		{"精确命中自定义 agent", []string{"recon_scout"}, "recon_scout", true},
		{"未命中其他 agent", []string{"worker"}, "planner", false},
		{"空身份不命中具体规则", []string{"worker"}, "", false},
		{"多选其一命中", []string{"worker", "planner"}, "planner", true},
		{"多选全不命中", []string{"worker", "planner"}, "review", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ruleAppliesToAgent(c.agents, c.agentName); got != c.want {
				t.Errorf("ruleAppliesToAgent(%v, %q) = %v, want %v", c.agents, c.agentName, got, c.want)
			}
		})
	}
}
