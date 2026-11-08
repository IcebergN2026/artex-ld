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
	matched  bool
	askOK    bool
}

func (s stubInterceptor) IsToolEnabled(string) bool { return true }
func (s stubInterceptor) Match(string, []byte) (intercept.Decision, bool) {
	return s.decision, s.matched
}
func (s stubInterceptor) Judge(context.Context, string, json.RawMessage) (intercept.Decision, bool) {
	return intercept.Decision{}, false
}
func (stubInterceptor) Log(context.Context, int64, intercept.Decision, string, []byte, string) {
}
func (s stubInterceptor) HandleAsk(context.Context, int64, intercept.Decision, string, []byte) bool {
	return s.askOK
}

// TestPreToolUseAuditCommandByDecision pins that every PreToolUse outcome logs
// exactly one Guard audit row carrying the shell command extracted from the same
// input the intercept rules saw. Block rows must retain the command even though
// the model-facing block message intentionally does not contain it.
func TestPreToolUseAuditCommandByDecision(t *testing.T) {
	const command = "echo guard-audit-probe"
	deny := intercept.Decision{Action: "deny", Message: "policy-probe"}
	allow := intercept.Decision{Action: "allow"}
	ask := intercept.Decision{Action: "ask", Message: "confirm-probe"}

	cases := []struct {
		name        string
		tool        string
		input       string
		ic          stubInterceptor
		cancel      bool
		wantBlocked bool
		wantAction  string
		wantCommand string
		wantReason  string
	}{
		{
			name: "deny Bash", tool: "Bash",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{decision: deny, matched: true},
			wantBlocked: true, wantAction: "block", wantCommand: command, wantReason: "policy-probe",
		},
		{
			name: "deny shell_open", tool: "shell_open",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{decision: deny, matched: true},
			wantBlocked: true, wantAction: "block", wantCommand: command, wantReason: "policy-probe",
		},
		{
			name: "deny shell_send", tool: "shell_send",
			input:       `{"text":"` + command + `"}`,
			ic:          stubInterceptor{decision: deny, matched: true},
			wantBlocked: true, wantAction: "block", wantCommand: command, wantReason: "policy-probe",
		},
		{
			name: "deny non-shell", tool: "Read",
			input:       `{"file_path":"/tmp/probe.txt","command":"not-a-shell-tool"}`,
			ic:          stubInterceptor{decision: deny, matched: true},
			wantBlocked: true, wantAction: "block", wantCommand: "", wantReason: "policy-probe",
		},
		{
			name: "ask approved Bash", tool: "Bash",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{decision: ask, matched: true, askOK: true},
			wantBlocked: false, wantAction: "allow", wantCommand: command,
		},
		{
			name: "ask rejected Bash", tool: "Bash",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{decision: ask, matched: true, askOK: false},
			wantBlocked: true, wantAction: "block", wantCommand: command, wantReason: "人工审批未通过",
		},
		{
			name: "ask cancelled Bash", tool: "Bash",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{decision: ask, matched: true, askOK: true},
			cancel:      true,
			wantBlocked: true, wantAction: "block", wantCommand: command, wantReason: "工作已取消",
		},
		{
			name: "allow Bash", tool: "Bash",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{decision: allow, matched: true},
			wantBlocked: false, wantAction: "allow", wantCommand: command,
		},
		{
			name: "no rule matched Bash", tool: "Bash",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{matched: false},
			wantBlocked: false, wantAction: "allow", wantCommand: command,
		},
		{
			name: "allow shell_open", tool: "shell_open",
			input:       `{"command":"` + command + `"}`,
			ic:          stubInterceptor{decision: allow, matched: true},
			wantBlocked: false, wantAction: "allow", wantCommand: command,
		},
		{
			name: "allow shell_send", tool: "shell_send",
			input:       `{"text":"` + command + `"}`,
			ic:          stubInterceptor{decision: allow, matched: true},
			wantBlocked: false, wantAction: "allow", wantCommand: command,
		},
		{
			name: "allow non-shell", tool: "Read",
			input:       `{"file_path":"/tmp/probe.txt","command":"not-a-shell-tool"}`,
			ic:          stubInterceptor{decision: allow, matched: true},
			wantBlocked: false, wantAction: "allow", wantCommand: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			g := newGuard(tc.ic)
			blocked, message, _ := g.Hooks().PreToolUse(ctx, tc.tool, json.RawMessage(tc.input))
			if blocked != tc.wantBlocked {
				t.Fatalf("blocked = %v, want %v (message %q)", blocked, tc.wantBlocked, message)
			}

			logs := g.Audit()
			if len(logs) != 1 {
				t.Fatalf("call produced %d Guard audit rows, want exactly one: %#v", len(logs), logs)
			}
			entry := logs[0]
			if entry.Tool != tc.tool {
				t.Errorf("audit tool = %q, want %q", entry.Tool, tc.tool)
			}
			if entry.Action != tc.wantAction {
				t.Errorf("audit action = %q, want %q", entry.Action, tc.wantAction)
			}
			if entry.Command != tc.wantCommand {
				t.Errorf("audit command = %q, want %q", entry.Command, tc.wantCommand)
			}
			if tc.wantReason != "" && !strings.Contains(entry.Reason, tc.wantReason) {
				t.Errorf("audit reason = %q, want substring %q", entry.Reason, tc.wantReason)
			}

			if tc.wantAction == "block" {
				if !strings.Contains(message, tc.wantReason) {
					t.Errorf("block message = %q, want substring %q", message, tc.wantReason)
				}
				if entry.Reason != message {
					t.Errorf("audit reason %q differs from block message %q", entry.Reason, message)
				}
				if tc.wantCommand != "" && strings.Contains(message, tc.wantCommand) {
					t.Errorf("block message must not expose the command %q: %q", tc.wantCommand, message)
				}
			} else if message != "" || entry.Reason != "" {
				t.Errorf("allow outcome carried a message/reason: message=%q reason=%q", message, entry.Reason)
			}
		})
	}
}

// TestPreToolUseAllowAndBlockRecordSameCommand checks the same tool input yields
// the same audit command whether the intercept rule allows or blocks it.
func TestPreToolUseAllowAndBlockRecordSameCommand(t *testing.T) {
	const command = "printf guard-audit-consistency"
	cases := []struct {
		tool  string
		input string
	}{
		{tool: "Bash", input: `{"command":"` + command + `"}`},
		{tool: "shell_open", input: `{"command":"` + command + `"}`},
		{tool: "shell_send", input: `{"text":"` + command + `"}`},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			input := json.RawMessage(tc.input)

			allowGuard := newGuard(stubInterceptor{
				decision: intercept.Decision{Action: "allow"},
				matched:  true,
			})
			if blocked, message, _ := allowGuard.Hooks().PreToolUse(context.Background(), tc.tool, input); blocked {
				t.Fatalf("allow rule blocked the call: %q", message)
			}

			blockGuard := newGuard(stubInterceptor{
				decision: intercept.Decision{Action: "deny", Message: "policy-probe"},
				matched:  true,
			})
			if blocked, _, _ := blockGuard.Hooks().PreToolUse(context.Background(), tc.tool, input); !blocked {
				t.Fatal("deny rule must block the tool call")
			}

			allowLogs, blockLogs := allowGuard.Audit(), blockGuard.Audit()
			if len(allowLogs) != 1 || len(blockLogs) != 1 {
				t.Fatalf("want exactly one audit row per call, got allow=%d block=%d", len(allowLogs), len(blockLogs))
			}
			if allowLogs[0].Command != command || blockLogs[0].Command != command {
				t.Errorf("audit command mismatch: allow=%q block=%q want %q",
					allowLogs[0].Command, blockLogs[0].Command, command)
			}
			if allowLogs[0].Command != blockLogs[0].Command {
				t.Errorf("same input recorded different commands: allow=%q block=%q",
					allowLogs[0].Command, blockLogs[0].Command)
			}
			if allowLogs[0].Action != "allow" || blockLogs[0].Action != "block" {
				t.Errorf("unexpected actions: allow=%q block=%q", allowLogs[0].Action, blockLogs[0].Action)
			}
		})
	}
}
