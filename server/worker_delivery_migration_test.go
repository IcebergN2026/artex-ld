package server

import (
	"strings"
	"testing"

	"artex/agent"
)

func TestInjectWorkerDeliveryFactSpec(t *testing.T) {
	legacy := "facts\n- **确认漏洞 → report_finding（探索图，传 intent_id）**：proof"
	got := injectWorkerDeliveryFactSpec(legacy)
	if !strings.Contains(got, agent.WorkerDeliveryFactSpec) || strings.Count(got, agent.WorkerDeliveryFactSpec) != 1 {
		t.Fatalf("delivery contract not injected exactly once: %q", got)
	}
	if again := injectWorkerDeliveryFactSpec(got); again != got {
		t.Fatal("injection must be idempotent")
	}
	custom := "fully custom prompt without the built-in anchor"
	if got := injectWorkerDeliveryFactSpec(custom); got != custom {
		t.Fatal("custom prompt must remain unchanged")
	}
}
