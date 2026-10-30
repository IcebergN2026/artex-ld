package agent

import "strings"

// ExecutionMode controls how a confirmed finding is interpreted by Goal,
// Planner and vuln_dispatcher. The zero/default mode preserves the original
// foothold-gated dispatcher behavior.
type ExecutionMode string

const (
	ExecutionModeFoothold          ExecutionMode = "foothold"
	ExecutionModeVulnerabilityOnly ExecutionMode = "vulnerability_only"
)

// ExecutionModeFromGoal recognizes only explicit vulnerability-only intent.
// Generic vulnerability validation is not enough: "验证 RCE" still uses the
// default mode, while "只验证 RCE，不需要 getshell" opts in. An explicit Shell
// or foothold request takes precedence when both kinds of intent are present.
func ExecutionModeFromGoal(text string) ExecutionMode {
	normalized := strings.ToLower(strings.NewReplacer(
		" ", "", "\t", "", "\n", "", "\r", "",
		"-", "", "_", "", "—", "", "–", "",
	).Replace(text))
	positive := normalized
	for _, negated := range []string{
		"不需要getshell", "无需getshell", "不要getshell",
		"不需要shell", "无需shell", "不要shell", "不建立shell",
	} {
		positive = strings.ReplaceAll(positive, negated, "")
	}
	for _, marker := range []string{
		"需要getshell", "需要shell", "建立立足点", "获得立足点",
		"反弹shell", "交互shell", "c2上线", "session上线",
		"footholdrequired", "interactiveshell", "reverseshell",
	} {
		if strings.Contains(positive, marker) {
			return ExecutionModeFoothold
		}
	}
	for _, marker := range []string{
		"[执行模式:仅漏洞路径]", "执行模式:仅漏洞路径", "仅漏洞路径",
		"只验证漏洞", "仅验证漏洞", "只复现漏洞", "仅复现漏洞",
		"只做漏洞验证", "仅做漏洞验证", "只确认漏洞", "仅确认漏洞",
		"不需要getshell", "无需getshell", "不要getshell",
		"不需要shell", "无需shell", "不要shell", "不建立shell",
		"vulnerabilityonly", "vulnerabilityvalidationonly", "poconly",
		"proofofconceptonly", "noshellrequired", "donotestablishshell",
	} {
		if strings.Contains(normalized, marker) {
			return ExecutionModeVulnerabilityOnly
		}
	}
	return ExecutionModeFoothold
}

func goalExecutionModeSpec(goal, description string) string {
	if ExecutionModeFromGoal(goal+"\n"+description) == ExecutionModeVulnerabilityOnly {
		return GoalVulnerabilityOnlySpec
	}
	return ""
}

// GoalVulnerabilityOnlySpec is code-owned prompt context. It is appended only
// for an explicitly recognized vulnerability-only task, so old/custom prompt
// templates do not need to duplicate this contract.
const GoalVulnerabilityOnlySpec = `

**执行模式：仅漏洞路径（已识别，最高优先级语义约束）**
- 本任务只要求确认、复现或解释用户指定漏洞及其边界；不要添加 getshell、WebShell、C2、持久化、内网移动或主机控制目标。
- 按实际漏洞类型设置可核验的完成条件：RCE 需要真实命令/代码执行证据；文件上传需要落地及访问/解析证据；其他漏洞按对应可复现证据验收。
- RCE、文件上传、反序列化或权限提升本身就是本模式的合法最终结果，不得升级为必须建立立足点。
- 只有用户同时明确要求 Shell/立足点时才退出本模式；当前识别结果表示没有这种要求。`

// PlannerVulnerabilityOnlySpec keeps planning focused on vulnerability
// evidence rather than weaponization or a mandatory foothold.
func plannerExecutionModeSpec(goal string) string {
	if ExecutionModeFromGoal(goal) == ExecutionModeVulnerabilityOnly {
		return PlannerVulnerabilityOnlySpec
	}
	return ""
}

const PlannerVulnerabilityOnlySpec = `

**执行模式：仅漏洞路径（代码按任务目标识别）**
- 只规划确认、复现和界定当前 Goal 指定漏洞所需的最少必要步骤，不把建立 Shell、WebShell、C2、持久化或内网移动设为目标或前置条件。
- 以漏洞对应的可复现实测证据验收：确认达到 Goal 后即可按现有 prove_goal 流程完成，不额外追加武器化或立足点意图。
- RCE 回显、文件上传落地/访问、反序列化链实证等可按当前 Goal 作为最终结果；不得把它们误写成已经建立 C2/交互会话。
- 若发现其他有验证价值的方向，只能在当前“仅漏洞路径”目标范围内规划，不自动扩大任务目标。`
