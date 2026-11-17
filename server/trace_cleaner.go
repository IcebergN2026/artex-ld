// trace_cleaner.go：预置痕迹清理 Agent 的幂等种子。
// Agent、提示词、工具绑定和显式派发触发器在同一事务中创建。
package server

import (
	"database/sql"
	"errors"
	"fmt"

	"artex/agent"
)

const (
	traceCleanerAgentKey       = "trace_cleaner"
	traceCleanerSeedFlag       = "trace_cleaner_seed_v1"
	traceCleanerDescription    = "由 planner 对符合清理条件的 finding 显式派发：核对本任务投放事实，列出清理计划并在真实用户明确确认后逐项执行。"
	traceCleanerFindingMessage = "上面刚有漏洞被 report_finding 登记（上下文给了 [vulnclass/severity] 与摘要）。按你的清理纪律处理：" +
		"仅 vulnclass ∈ {getshell,webshell,获取目标权限,rce} 且 severity ∈ {critical,high}（=已获得权限/立足点）才进入清理审计；" +
		"先用 list_task_findings 确认、用 godzilla shell_test 验证立足点活性，再用 get_task_graph 收集该任务全部「[投放]」事实逐项清理；" +
		"其余一律不清，直接结束。同一主机已有「[清理]」汇总事实的不重复清理。"
)

// seedTraceCleanerAgent seeds the optional cleaner once without requiring a
// config-export import. A pre-existing key is preserved as-is. The settings flag
// ensures a later user deletion is not undone by a restart.
func (s *Server) seedTraceCleanerAgent() error {
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(7337741011)`); err != nil {
		return err
	}

	var done string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, traceCleanerSeedFlag).Scan(&done)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}

	var agentID int64
	err = tx.QueryRow(`INSERT INTO agents(key,name,description,role,builtin,enabled)
		VALUES ($1,'痕迹清理',$2,'assistant',false,true)
		ON CONFLICT (key) DO NOTHING RETURNING id`, traceCleanerAgentKey, traceCleanerDescription).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil // A user-defined agent already owns the key; do not overwrite it.
	}
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	if agentID > 0 {
		var promptID int64
		if err = tx.QueryRow(`INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by)
			VALUES ($1,1,$2,'内置默认','system') RETURNING id`, agentID, agent.TraceCleanerDefaultPrompt).Scan(&promptID); err != nil {
			return fmt.Errorf("seed prompt: %w", err)
		}
		if _, err = tx.Exec(`UPDATE agents SET current_prompt_id=$1,
			trigger_run_mode='serial', trigger_merge_mode='by_task', trigger_max_parallel=0
			WHERE id=$2`, promptID, agentID); err != nil {
			return fmt.Errorf("configure agent: %w", err)
		}

		for _, toolKey := range []string{"get_task_graph", "get_task_node_detail", "list_task_findings", "record_fact"} {
			if _, err = tx.Exec(`UPDATE tools SET agents=agents || to_jsonb($2::text)
				WHERE key=$1 AND NOT (agents ? $2)`, toolKey, traceCleanerAgentKey); err != nil {
				return fmt.Errorf("bind tool %s: %w", toolKey, err)
			}
			var bound bool
			if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tools WHERE key=$1 AND agents ? $2)`,
				toolKey, traceCleanerAgentKey).Scan(&bound); err != nil {
				return fmt.Errorf("verify tool %s: %w", toolKey, err)
			}
			if !bound {
				return fmt.Errorf("required tool %q is not seeded", toolKey)
			}
		}

		if _, err = tx.Exec(`INSERT INTO agent_triggers(agent_key,enabled,on_finding,finding_message)
			VALUES ($1,true,false,$2)`, traceCleanerAgentKey, traceCleanerFindingMessage); err != nil {
			return fmt.Errorf("seed explicit-dispatch trigger: %w", err)
		}
	}

	if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'true')
		ON CONFLICT(key) DO UPDATE SET value='true'`, traceCleanerSeedFlag); err != nil {
		return fmt.Errorf("mark seed complete: %w", err)
	}
	return tx.Commit()
}
