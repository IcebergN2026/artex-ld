// finding_orchestration_migration.go：把 Finding 下游 Agent 从全局订阅迁移到 Planner 显式派发。
// 主要函数/类型：
//   - upgradeVulnDispatcherFindingPrompt：只刷新旧默认血统的派发提示词，自定义血统不动。
//   - upgradeVulnDispatcherIndependentGraph：只刷新仍要求继承整条来源任务的旧默认血统。
//   - upgradePlannerFindingDispatch：关闭 vuln_dispatcher/trace_cleaner 的旧全局 Finding 入口。

package server

import (
	"log"
	"strings"

	"artex/agent"
)

// upgradeVulnDispatcherFindingPrompt refreshes only the old default lineage.
// Custom prompt lineage is left untouched.
func (s *Server) upgradeVulnDispatcherFindingPrompt() {
	const flag = "vuln_dispatcher_prompt_planner_gate_v1"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	a, err := s.m.pg.GetAgentByKey("vuln_dispatcher")
	if err != nil {
		log.Printf("[vuln_dispatcher] 读取 Planner 派发提示词迁移对象失败: %v", err)
		return
	}
	if a != nil {
		current, err := s.m.pg.CurrentPrompt(a.ID)
		if err != nil {
			log.Printf("[vuln_dispatcher] 读取当前提示词失败: %v", err)
			return
		}
		defaultLineage := strings.Contains(current, "漏洞派发 agent") &&
			strings.Contains(current, "审核开关由人决定") && strings.Contains(current, "去重的键是")
		if defaultLineage && !strings.Contains(current, "dispatch_finding_to_agent 显式调用") {
			if _, err := s.m.pg.ResetPromptToDefault(a.ID, s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt)); err != nil {
				log.Printf("[vuln_dispatcher] Planner 派发提示词迁移失败: %v", err)
				return
			}
		}
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[vuln_dispatcher] Planner 派发提示词迁移标记失败: %v", err)
	}
}

// upgradeVulnDispatcherIndependentGraph refreshes only the default lineage
// that still instructs spawn_task to inherit the whole source task.
func (s *Server) upgradeVulnDispatcherIndependentGraph() {
	const flag = "vuln_dispatcher_independent_graph_v1"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	a, err := s.m.pg.GetAgentByKey("vuln_dispatcher")
	if err != nil {
		log.Printf("[vuln_dispatcher] 读取独立探索链提示词迁移对象失败: %v", err)
		return
	}
	if a != nil {
		current, err := s.m.pg.CurrentPrompt(a.ID)
		if err != nil {
			log.Printf("[vuln_dispatcher] 读取当前提示词失败: %v", err)
			return
		}
		defaultLineage := strings.Contains(current, "漏洞派发 agent") &&
			strings.Contains(current, "审核开关由人决定") && strings.Contains(current, "去重的键是")
		if defaultLineage && strings.Contains(current, "source_task_ids = [触发任务 id]") {
			if _, err := s.m.pg.ResetPromptToDefault(a.ID, s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt)); err != nil {
				log.Printf("[vuln_dispatcher] 独立探索链提示词迁移失败: %v", err)
				return
			}
		}
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[vuln_dispatcher] 独立探索链提示词迁移标记失败: %v", err)
	}
}

// upgradePlannerFindingDispatch disables only the two legacy global Finding
// subscriptions. Enabled, messages, other trigger booleans and run policy stay
// unchanged; administrators can explicitly re-enable them later in the UI.
func (s *Server) upgradePlannerFindingDispatch() {
	const flag = "planner_finding_orchestration_v1"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	if _, err := s.m.pg.Exec(`UPDATE agents SET description=$1 WHERE key='vuln_dispatcher' AND description=$2`, vulnDispatcherDescription, vulnDispatcherDescriptionV1); err != nil {
		log.Printf("[finding-dispatch] 更新 Dispatcher 描述失败: %v", err)
		return
	}
	for _, agentKey := range []string{"vuln_dispatcher", "trace_cleaner"} {
		triggers, err := s.m.pg.ListTriggersFor(agentKey)
		if err != nil {
			log.Printf("[finding-dispatch] 读取触发器失败: agent=%s err=%v", agentKey, err)
			return
		}
		for _, tr := range triggers {
			changed := false
			if agentKey == "vuln_dispatcher" && tr.FindingMessage == vulnDispatcherFindingMessageV2 {
				tr.FindingMessage = vulnDispatcherFindingMessage
				changed = true
			}
			if tr.OnFinding {
				tr.OnFinding = false
				changed = true
			}
			if !changed {
				continue
			}
			if err := s.m.pg.UpdateTrigger(tr); err != nil {
				log.Printf("[finding-dispatch] 关闭旧 Finding 入口失败: agent=%s trigger=%d err=%v", agentKey, tr.ID, err)
				return
			}
		}
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[finding-dispatch] 写入编排迁移标记失败: %v", err)
	}
}
