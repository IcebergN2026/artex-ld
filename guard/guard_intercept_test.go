package guard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"artex/intercept"
)

type stubInterceptor struct {
	decision intercept.Decision
}

func (s stubInterceptor) IsToolEnabled(string) bool { return true }
func (s stubInterceptor) Match(context.Context, string, []byte) (intercept.Decision, bool) {
	return s.decision, true
}
func (s stubInterceptor) Judge(context.Context, string, json.RawMessage) (intercept.Decision, bool) {
	return intercept.Decision{}, false
}
func (stubInterceptor) Log(context.Context, int64, intercept.Decision, string, []byte, string) {
}
func (stubInterceptor) HandleAsk(context.Context, int64, intercept.Decision, string, []byte) bool {
	return true
}

// captureInterceptor 记录 Match/Judge 收到的工具名，验证 ExecuteExtraTool 解包。
type captureInterceptor struct {
	stubInterceptor
	matchTool string
}

func (s *captureInterceptor) Match(_ context.Context, tool string, _ []byte) (intercept.Decision, bool) {
	s.matchTool = tool
	return s.decision, true
}

// MCP/扩展工具经 ExecuteExtraTool 延迟调用：规则必须按入参 tool_name 的真实工具名
// 匹配，否则 ^mcp__… 之类的 tool_name 规则永远命中不了包装器名字。
func TestPreToolUseExecuteExtraToolMatchesInnerToolName(t *testing.T) {
	ic := &captureInterceptor{stubInterceptor: stubInterceptor{decision: intercept.Decision{Action: "deny", Message: "webshell blocked"}}}
	g := newGuard(ic)
	input := json.RawMessage(`{"tool_name":"mcp__godzilla__shell_exec","params":{"cmd":"id"}}`)
	blocked, _, _ := g.Hooks().PreToolUse(context.Background(), "ExecuteExtraTool", input)
	if !blocked {
		t.Fatal("inner mcp tool deny must block the ExecuteExtraTool call")
	}
	if ic.matchTool != "mcp__godzilla__shell_exec" {
		t.Fatalf("Match got tool %q, want inner tool name", ic.matchTool)
	}
}

// 非包装器调用不得解包：普通工具按原名进入 Match。
func TestPreToolUsePlainToolNameUnchanged(t *testing.T) {
	ic := &captureInterceptor{stubInterceptor: stubInterceptor{decision: intercept.Decision{Action: "deny", Message: "x"}}}
	g := newGuard(ic)
	g.Hooks().PreToolUse(context.Background(), "Bash", json.RawMessage(`{"command":"id"}`))
	if ic.matchTool != "Bash" {
		t.Fatalf("Match got tool %q, want Bash", ic.matchTool)
	}
}

func TestPreToolUseDeniedCallHasOnlyBlockAudit(t *testing.T) {
	g := newGuard(stubInterceptor{decision: intercept.Decision{Action: "deny", Message: "policy"}})
	blocked, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", json.RawMessage(`{"command":"rm -rf /tmp/example"}`))
	if !blocked {
		t.Fatal("deny rule must block the tool call")
	}
	logs := g.Audit()
	if len(logs) != 1 {
		t.Fatalf("denied call produced %d audit rows, want exactly one: %#v", len(logs), logs)
	}
	if logs[0].Action != "block" || !strings.Contains(logs[0].Reason, "policy") {
		t.Fatalf("unexpected audit row: %#v", logs[0])
	}
}
