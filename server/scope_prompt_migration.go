// scope_prompt_migration.go：把「无范围限制」改造刷入存量库的提示词/模板回滚为严格授权版本。
// 主要函数/类型：
//   - upgradeScopeAuthorizationPrompts：按新迁移标记幂等回滚（不受 unrestricted_agent_prompts_v2 影响）。
//   - restoreScopeScenarioTemplates：恢复被无范围限制迁移改写的内置 Planner/Worker 场景模板。
//   - restoreSystemManagedPrompt：只把 updated_by='system' 的当前提示词追加新版本恢复为默认。

package server

import (
	"database/sql"
	"fmt"
	"strings"

	"artex/agent"
	db "artex/db"
)

// scopeAuthorizationPromptMigrationKey 是独立的严格范围回滚标记。旧的无范围限制迁移
// 使用 unrestricted_agent_prompts_v2；这里必须用新 key，存量库即使 v2=done 也要执行一次。
const scopeAuthorizationPromptMigrationKey = "scope_authorization_prompts_v3"

// upgradeScopeAuthorizationPrompts rolls the built-in scope prompts back to the
// strict authorization defaults once. It is keyed independently from the retired
// unrestricted migration, so databases that already ran
// unrestricted_agent_prompts_v2=done still get this rollback exactly once.
//
// Ownership rules:
//   - planner_templates/worker_templates: only builtin rows touched by the old
//     unrestricted migration (the five scenario nkeys) are restored. Custom
//     (builtin=false) templates and the external-pentest default templates (which
//     that migration never rewrote) are left alone.
//   - agent_prompts: only the current version whose updated_by='system' is
//     eligible. User edits (updated_by='ui' and any other non-system owner) are
//     preserved. Restoration appends a new version via ResetPromptToDefault, so
//     the unrestricted prompt remains in history.
//   - goals is deliberately absent: it stays byte-identical to the upstream
//     native prompt.
//   - tasks.planner_prompt_override / tasks.worker_prompt_override are NOT
//     rewritten: historical snapshots have no reliable lineage/ownership
//     metadata, so bulk rewriting would clobber user changes. The runtime still
//     appends the current task_constraints block to planner/worker system prompts
//     (see agent/constraints.go), so the strict scope boundary keeps applying to
//     existing tasks through their live constraints.
//
// The marker is written only after every step succeeds; a failed run returns the
// error and remains eligible for the next startup.
func upgradeScopeAuthorizationPrompts(pg *db.DB) error {
	_, done, err := pg.GetSetting(scopeAuthorizationPromptMigrationKey)
	if err != nil || done {
		return err
	}
	if err := applyScopeAuthorizationRollback(pg); err != nil {
		return err
	}
	return pg.SetSetting(scopeAuthorizationPromptMigrationKey, "done")
}

func applyScopeAuthorizationRollback(pg *db.DB) error {
	if err := restoreScopeScenarioTemplates(pg); err != nil {
		return err
	}
	seeds := agent.BuiltinPromptSeeds()
	// Refresh every built-in agent's current system-managed prompt. goals is
	// deliberately excluded: it stays byte-identical to the upstream native
	// prompt. User edits are protected by updated_by below; non-builtin custom
	// agents are not part of this loop.
	for _, key := range []string{"auto", "mainagent", "pentest", "planner", "review", "worker"} {
		body := seeds[key]
		if body == "" {
			return fmt.Errorf("内置提示词种子缺失: %s", key)
		}
		a, err := pg.GetAgentByKey(key)
		if err != nil {
			return fmt.Errorf("读取 %s Agent: %w", key, err)
		}
		if a == nil || !a.Builtin {
			continue
		}
		if err := restoreSystemManagedPrompt(pg, a.ID, key, body); err != nil {
			return err
		}
	}
	// vuln_dispatcher is a system-seeded custom agent (agents.builtin=false), but
	// its current prompt was also rewritten by the unrestricted migration. The
	// updated_by='system' guard keeps user customizations intact.
	a, err := pg.GetAgentByKey("vuln_dispatcher")
	if err != nil {
		return fmt.Errorf("读取 vuln_dispatcher Agent: %w", err)
	}
	if a != nil {
		if err := restoreSystemManagedPrompt(pg, a.ID, "vuln_dispatcher", agent.VulnDispatcherDefaultPrompt); err != nil {
			return err
		}
	}
	return restoreDispatcherConstraintMessage(pg)
}

// restoreScopeScenarioTemplates refreshes the five built-in scenario templates
// that the unrestricted migration rewrote. The external-pentest default template
// is not in that set and is intentionally not overwritten; custom templates
// (builtin=false) are never touched.
func restoreScopeScenarioTemplates(pg *db.DB) error {
	for nkey, body := range agent.PlannerDomainSeeds() {
		if _, err := pg.Exec(`UPDATE planner_templates SET body=$1,updated_at=now() WHERE builtin AND nkey=$2 AND body<>$1`, body, nkey); err != nil {
			return fmt.Errorf("恢复内置 Planner 模板 %s: %w", nkey, err)
		}
	}
	for _, seed := range agent.WorkerDomainSeeds() {
		if _, err := pg.Exec(`UPDATE worker_templates SET body=$1,updated_at=now() WHERE builtin AND nkey=$2 AND body<>$1`, seed.Body, seed.NKey); err != nil {
			return fmt.Errorf("恢复内置 Worker 模板 %s: %w", seed.NKey, err)
		}
	}
	return nil
}

// restoreSystemManagedPrompt rewrites only a system-owned current prompt. User
// edits (updated_by != 'system') are skipped, and the strict default is appended
// as a new version instead of overwriting the existing row, preserving history.
func restoreSystemManagedPrompt(pg *db.DB, agentID int64, key, body string) error {
	var current, updatedBy sql.NullString
	err := pg.QueryRow(`
SELECT p.template_text, p.updated_by
FROM agents a
LEFT JOIN agent_prompts p ON p.id=a.current_prompt_id
WHERE a.id=$1`, agentID).Scan(&current, &updatedBy)
	if err != nil {
		return fmt.Errorf("读取 %s 当前提示词: %w", key, err)
	}
	if !current.Valid {
		return nil // no prompt version yet; startup seeding handles fresh rows
	}
	if !strings.EqualFold(strings.TrimSpace(updatedBy.String), "system") {
		return nil // user custom prompt: never overwrite
	}
	if current.String == body {
		return nil
	}
	if _, err := pg.ResetPromptToDefault(agentID, body); err != nil {
		return fmt.Errorf("恢复 %s 系统默认提示词: %w", key, err)
	}
	return nil
}

// restoreDispatcherConstraintMessage updates only the exact system default used
// before the rollback. Custom trigger messages are left untouched.
func restoreDispatcherConstraintMessage(pg *db.DB) error {
	triggers, err := pg.ListTriggersFor("vuln_dispatcher")
	if err != nil {
		return fmt.Errorf("读取 vuln_dispatcher 触发器: %w", err)
	}
	for _, trigger := range triggers {
		if trigger.FindingMessage != vulnDispatcherFindingMessageUnrestricted {
			continue
		}
		trigger.FindingMessage = vulnDispatcherFindingMessage
		if err := pg.UpdateTrigger(trigger); err != nil {
			return fmt.Errorf("恢复 vuln_dispatcher 约束检查消息: %w", err)
		}
	}
	return nil
}
