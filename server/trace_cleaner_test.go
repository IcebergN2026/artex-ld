package server

import (
	"strings"
	"testing"

	"artex/agent"
)

func TestSeedTraceCleanerAgent(t *testing.T) {
	s, m := findingMigrationServer(t)
	pg := m.pg
	t.Cleanup(snapshotSettingsTable(t, pg))
	t.Cleanup(snapshotToolsTable(t, pg))

	_, _ = pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, traceCleanerAgentKey)
	_ = pg.RemoveAgentFromToolBindings(traceCleanerAgentKey)
	_ = pg.DeleteAgent(traceCleanerAgentKey)
	_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, traceCleanerSeedFlag)
	t.Cleanup(func() {
		_, _ = pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, traceCleanerAgentKey)
		_ = pg.RemoveAgentFromToolBindings(traceCleanerAgentKey)
		_ = pg.DeleteAgent(traceCleanerAgentKey)
	})

	for _, key := range []string{"get_task_graph", "get_task_node_detail", "list_task_findings", "record_fact"} {
		tool, err := pg.GetTool(key)
		if err != nil {
			t.Fatal(err)
		}
		if tool == nil {
			if err := pg.SeedTool(key, "test prerequisite", nil, nil); err != nil {
				t.Fatal(err)
			}
			key := key
			t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, key) })
		}
	}

	if err := s.seedTraceCleanerAgent(); err != nil {
		t.Fatal(err)
	}
	a, err := pg.GetAgentByKey(traceCleanerAgentKey)
	if err != nil || a == nil {
		t.Fatalf("seeded agent missing: %v", err)
	}
	if a.Builtin || !a.Enabled || a.TriggerRunMode != "serial" || a.TriggerMergeMode != "by_task" || a.TriggerMaxParallel != 0 {
		t.Fatalf("unexpected agent defaults: %+v", a)
	}
	if !strings.Contains(a.Description, "显式派发") {
		t.Fatalf("description does not expose the explicit-dispatch entry: %q", a.Description)
	}
	prompt, err := pg.CurrentPrompt(a.ID)
	if err != nil || prompt != agent.TraceCleanerDefaultPrompt {
		t.Fatalf("default prompt mismatch: %v", err)
	}

	triggers, err := pg.ListTriggersFor(traceCleanerAgentKey)
	if err != nil || len(triggers) != 1 {
		t.Fatalf("expected one explicit-dispatch trigger: %+v, %v", triggers, err)
	}
	tr := triggers[0]
	if !tr.Enabled || tr.OnFinding || tr.FindingMessage != traceCleanerFindingMessage {
		t.Fatalf("unexpected trigger: %+v", tr)
	}
	for _, key := range []string{"get_task_graph", "get_task_node_detail", "list_task_findings", "record_fact"} {
		tool, err := pg.GetTool(key)
		if err != nil || tool == nil || !contains(tool.Agents, traceCleanerAgentKey) {
			t.Fatalf("tool %s is not bound to cleaner: %+v, %v", key, tool, err)
		}
	}

	// A restart must not duplicate rows or replace a user's edited prompt.
	if err := s.seedTraceCleanerAgent(); err != nil {
		t.Fatal(err)
	}
	var promptCount, triggerCount int
	if err := pg.QueryRow(`SELECT count(*) FROM agent_prompts WHERE agent_id=$1`, a.ID).Scan(&promptCount); err != nil || promptCount != 1 {
		t.Fatalf("restart duplicated prompts: %d, %v", promptCount, err)
	}
	if err := pg.QueryRow(`SELECT count(*) FROM agent_triggers WHERE agent_key=$1`, traceCleanerAgentKey).Scan(&triggerCount); err != nil || triggerCount != 1 {
		t.Fatalf("restart duplicated triggers: %d, %v", triggerCount, err)
	}
	if _, err := pg.SavePrompt(a.ID, "user customized cleaner prompt", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.seedTraceCleanerAgent(); err != nil {
		t.Fatal(err)
	}
	if got, _ := pg.CurrentPrompt(a.ID); got != "user customized cleaner prompt" {
		t.Fatalf("restart overwrote user prompt: %q", got)
	}

	// The one-time seed must not resurrect an intentionally deleted agent.
	_, _ = pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, traceCleanerAgentKey)
	if err := pg.DeleteAgent(traceCleanerAgentKey); err != nil {
		t.Fatal(err)
	}
	if err := s.seedTraceCleanerAgent(); err != nil {
		t.Fatal(err)
	}
	if got, err := pg.GetAgentByKey(traceCleanerAgentKey); err != nil || got != nil {
		t.Fatalf("deleted agent was resurrected: %+v, %v", got, err)
	}
}
