// finding_prompt_migration.go：把 Finding 证据与显式派发规则升级到存量外网渗透内置提示词。
// 主要函数/类型：
//   - upgradeExternalFindingPrompts：一次性迁移入口，全部步骤成功后才写完成标记。
//   - applyExternalFindingPrompts：更新内置 Planner/Worker 提示词、当前版本与逐字一致的旧任务快照。

package server

import (
	"fmt"
	"log"

	"artex/agent"
	"artex/db"
)

// upgradeExternalFindingPrompts updates the built-in external-pentest prompts,
// their current system-prompt versions, and only those task snapshots that are
// byte-identical to the previous built-in bodies. Custom templates and custom
// snapshots are not changed. The completion flag is written only after every
// step succeeds, so a failed startup migration is retried.
func (s *Server) upgradeExternalFindingPrompts() {
	const flag = "external_finding_system_prompts_v4"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	if err := s.applyExternalFindingPrompts(); err != nil {
		log.Printf("[prompts] Finding 内置提示词迁移失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[prompts] Finding 内置提示词迁移标记写入失败: %v", err)
	}
}

func (s *Server) applyExternalFindingPrompts() error {
	pg := s.m.pg
	plannerTemplate, err := pg.GetPlannerTemplateByNKey(db.ExternalPentestTemplateName)
	if err != nil {
		return fmt.Errorf("读取外网 Planner 模板: %w", err)
	}
	workerTemplate, err := pg.GetWorkerTemplateByNKey(db.ExternalPentestTemplateName)
	if err != nil {
		return fmt.Errorf("读取外网 Worker 模板: %w", err)
	}
	if plannerTemplate == nil || workerTemplate == nil {
		return fmt.Errorf("外网渗透内置模板尚未初始化")
	}
	seeds := agent.BuiltinPromptSeeds()
	for _, key := range []string{"planner", "worker"} {
		a, err := pg.GetAgentByKey(key)
		if err != nil {
			return fmt.Errorf("读取 %s Agent: %w", key, err)
		}
		if a == nil {
			return fmt.Errorf("%s Agent 尚未初始化", key)
		}
		current, err := pg.CurrentPrompt(a.ID)
		if err != nil {
			return fmt.Errorf("读取 %s 当前提示词: %w", key, err)
		}
		if current != seeds[key] {
			if _, err := pg.ResetPromptToDefault(a.ID, seeds[key]); err != nil {
				return fmt.Errorf("升级 %s 当前提示词: %w", key, err)
			}
		}
	}

	tx, err := pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE planner_templates SET body=$1,updated_at=now() WHERE builtin AND nkey=$2`, seeds["planner"], db.ExternalPentestTemplateName); err != nil {
		return fmt.Errorf("升级外网 Planner 模板: %w", err)
	}
	if _, err := tx.Exec(`UPDATE worker_templates SET body=$1,updated_at=now() WHERE builtin AND nkey=$2`, seeds["worker"], db.ExternalPentestTemplateName); err != nil {
		return fmt.Errorf("升级外网 Worker 模板: %w", err)
	}
	if plannerTemplate.Body != "" && plannerTemplate.Body != seeds["planner"] {
		if _, err := tx.Exec(`UPDATE tasks SET planner_prompt_override=$1 WHERE planner_prompt_override=$2`, seeds["planner"], plannerTemplate.Body); err != nil {
			return fmt.Errorf("升级默认 Planner 任务快照: %w", err)
		}
	}
	if workerTemplate.Body != "" && workerTemplate.Body != seeds["worker"] {
		if _, err := tx.Exec(`UPDATE tasks SET worker_prompt_override=$1 WHERE worker_prompt_override=$2`, seeds["worker"], workerTemplate.Body); err != nil {
			return fmt.Errorf("升级默认 Worker 任务快照: %w", err)
		}
	}
	return tx.Commit()
}
