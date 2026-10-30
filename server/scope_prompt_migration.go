package server

import (
	"fmt"

	"github.com/Autumn-27/artex/agent"
	db "github.com/Autumn-27/artex/db"
)

const scopeAuthorizationPromptMigrationKey = "unrestricted_agent_prompts_v2"

// upgradeScopeAuthorizationPrompts removes the retired scope-authorization
// wording. Built-in scenario templates are refreshed once; custom templates
// (builtin=false) and all existing task prompt snapshots are left untouched.
func upgradeScopeAuthorizationPrompts(pg *db.DB) error {
	_, ok, err := pg.GetSetting(scopeAuthorizationPromptMigrationKey)
	if err != nil || ok {
		return err
	}
	for nkey, body := range agent.PlannerDomainSeeds() {
		if _, err := pg.Exec(`UPDATE planner_templates SET body=$1,updated_at=now() WHERE builtin AND nkey=$2 AND body<>$1`, body, nkey); err != nil {
			return fmt.Errorf("upgrade unrestricted planner %s: %w", nkey, err)
		}
	}
	for _, seed := range agent.WorkerDomainSeeds() {
		if _, err := pg.Exec(`UPDATE worker_templates SET body=$1,updated_at=now() WHERE builtin AND nkey=$2 AND body<>$1`, seed.Body, seed.NKey); err != nil {
			return fmt.Errorf("upgrade unrestricted worker %s: %w", seed.NKey, err)
		}
	}
	// Refresh the affected current prompts. The goals agent is deliberately
	// absent: its prompt stays byte-identical to the GitHub-native version.
	seeds := agent.BuiltinPromptSeeds()
	seeds["vuln_dispatcher"] = agent.VulnDispatcherDefaultPrompt
	for _, key := range []string{"auto", "pentest", "planner", "worker", "vuln_dispatcher"} {
		body := seeds[key]
		if _, err := pg.Exec(`UPDATE agent_prompts p SET template_text=$1,updated_by='system' FROM agents a WHERE p.agent_id=a.id AND a.current_prompt_id=p.id AND a.key=$2 AND p.template_text<>$1`, body, key); err != nil {
			return fmt.Errorf("upgrade current unrestricted prompt %s: %w", key, err)
		}
	}
	return pg.SetSetting(scopeAuthorizationPromptMigrationKey, "done")
}
