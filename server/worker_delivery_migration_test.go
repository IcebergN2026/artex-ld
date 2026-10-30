package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
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

func TestWorkerDeliveryConfigExportMatchesDefault(t *testing.T) {
	for _, line := range readExportLines(t, "../config-export/agent_prompts_current.jsonl") {
		var row struct {
			Key          string `json:"agent_key"`
			TemplateText string `json:"template_text"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Key != "worker" {
			continue
		}
		if row.TemplateText != agent.BuiltinPromptSeeds()["worker"] {
			t.Fatal("exported worker prompt must match the built-in delivery contract")
		}
		return
	}
	t.Fatal("worker prompt missing from config export")
}
