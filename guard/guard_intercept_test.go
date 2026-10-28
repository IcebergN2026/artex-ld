package guard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/intercept"
)

type stubInterceptor struct {
	decision intercept.Decision
}

func (s stubInterceptor) IsToolEnabled(string) bool { return true }
func (s stubInterceptor) Match(string, []byte) (intercept.Decision, bool) {
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
