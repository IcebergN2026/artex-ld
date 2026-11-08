package server

import (
	"strings"
	"testing"

	"artex/agent"
)

// TestVulnDispatcherRequiresEstablishedFoothold 验证 dispatcher 的行为契约：
// foothold 模式必须有可继续操作的立足点证据；授权边界（goal 禁止行/deny 约束）必须先检查；
// 破坏性操作（删改数据、DoS、持久化后门）始终禁止。只断言稳定的短契约标记，
// 不重新钉住整段措辞，避免把安全纪律的合法表述误判为回归。
func TestVulnDispatcherRequiresEstablishedFoothold(t *testing.T) {
	prompt := agent.VulnDispatcherDefaultPrompt
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "webshell foothold", want: "vulnclass=webshell"},
		{name: "webshell verified", want: "可访问或可连接"},
		{name: "C2 foothold", want: "C2/反弹会话已经上线"},
		{name: "RCE alone insufficient", want: "仅证明 RCE、仅上传木马但未上线不算"},
		{name: "no dispatch without foothold", want: "不要调用 spawn_task"},
		{name: "constraint check", want: "操作约束检查"},
		{name: "deny constraint gate", want: "deny 约束"},
		{name: "forbidden direction not dispatched", want: "不创建该方向的子任务"},
		{name: "forbidden direction never derived", want: "明确禁止的方向绝不派生"},
		{name: "explicit authorization only", want: "禁止测试任何未明确授权的其他主机"},
		{name: "reachability is not authorization", want: "直连可达或同网段"},
		{name: "no self-relaxation", want: "不自行放松"},
		{name: "destructive ops forbidden", want: "禁止破坏性操作"},
		{name: "data deletion forbidden", want: "删改数据"},
		{name: "DoS forbidden", want: "DoS"},
		{name: "persistence backdoor forbidden", want: "持久化后门"},
	} {
		if !strings.Contains(prompt, tc.want) {
			t.Errorf("dispatcher prompt missing %s contract marker %q", tc.name, tc.want)
		}
	}
}
