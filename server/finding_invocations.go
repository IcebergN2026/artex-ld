// finding_invocations.go：Planner 显式派发 Finding 给自定义 Agent 的通用工具与统一入队路径。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"artex/agent"
	"artex/db"
	actool "norma/tool"
)

func (s *Server) toolDispatchFindingToAgent() actool.CoreTool {
	desc := "把当前 Planner 任务中的一条 Finding 显式派发给一个已存在、启用且非内置的自定义 Agent。finding_id 是独立漏洞记录 ID（findings.id），不是探索节点 ID finding_node_id。同一 agent_key+finding_id 只记录一次调用；already_invoked 返回首次会话 ID，retry_scheduled 表示尚未创建会话并等待兜底补发。enqueued/retry_scheduled 均不表示下游 Agent 已执行或完成。具体场景允许的目标和证据前提由调用方系统提示词决定。"
	schema := objSchema(map[string]any{
		"agent_key":  strParam("目标自定义 Agent 的 key"),
		"finding_id": map[string]any{"type": "integer", "description": "当前任务中的独立漏洞记录 ID（findings.id）"},
		"reason":     strParam("Planner 确认的证据前提和本次派发原因"),
	}, "agent_key", "finding_id", "reason")
	return wrTool("dispatch_finding_to_agent", desc, schema, func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		ri := agent.RunInfoFrom(ctx)
		if ri.TaskID == 0 {
			return actool.Errorf("缺少 planner 任务上下文"), nil
		}
		var args struct {
			AgentKey  string          `json:"agent_key"`
			FindingID json.RawMessage `json:"finding_id"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal(in, &args); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		args.AgentKey = strings.TrimSpace(args.AgentKey)
		args.Reason = strings.TrimSpace(args.Reason)
		findingID := parseProfileID(args.FindingID)
		if args.AgentKey == "" || findingID <= 0 || args.Reason == "" {
			return actool.Errorf("agent_key、有效的 finding_id 和非空 reason 为必填"), nil
		}
		finding, err := s.m.pg.GetFinding(findingID)
		if err != nil {
			return actool.Errorf("读取 Finding 失败: " + err.Error()), nil
		}
		if finding == nil {
			return actool.Errorf("finding 不存在"), nil
		}
		if finding.TaskID == nil || *finding.TaskID != ri.TaskID {
			return actool.Errorf("finding 不属于当前 planner 任务"), nil
		}
		target, err := s.m.pg.GetAgentByKey(args.AgentKey)
		if err != nil {
			return actool.Errorf("读取目标 Agent 失败: " + err.Error()), nil
		}
		if target == nil {
			return actool.Errorf("目标 Agent 不存在"), nil
		}
		if !target.Enabled {
			return actool.Errorf("目标 Agent 已禁用"), nil
		}
		if target.Builtin {
			return actool.Errorf("目标 Agent 不能是内置 Agent"), nil
		}
		inv, inserted, err := s.m.pg.RecordFindingInvocation(args.AgentKey, findingID, ri.TaskID, args.Reason)
		if err != nil {
			return actool.Errorf("记录 Finding 派发失败: " + err.Error()), nil
		}
		if !inserted {
			if inv.ConversationID != nil {
				return jsonResult(map[string]any{
					"status": "already_invoked", "agent_key": inv.AgentKey,
					"finding_id": inv.FindingID, "conversation_id": *inv.ConversationID,
				})
			}
			return jsonResult(map[string]any{
				"status": "retry_scheduled", "agent_key": inv.AgentKey, "finding_id": inv.FindingID,
			})
		}
		accepted, err := s.enqueueFindingInvocation(inv)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		status := "retry_scheduled"
		if accepted {
			status = "enqueued"
		}
		return jsonResult(map[string]any{
			"status": status, "agent_key": inv.AgentKey, "finding_id": inv.FindingID,
		})
	})
}

// enqueueFindingInvocation is shared by the initial tool call and the durable
// retry sweep. It revalidates all mutable state before using the trigger queue.
func (s *Server) enqueueFindingInvocation(inv db.FindingInvocation) (bool, error) {
	if s.m.pg == nil {
		return false, errors.New("数据库未初始化")
	}
	finding, err := s.m.pg.GetFinding(inv.FindingID)
	if err != nil {
		return false, fmt.Errorf("读取 Finding 失败: %w", err)
	}
	if finding == nil {
		return false, errors.New("finding 不存在")
	}
	if finding.TaskID == nil || *finding.TaskID != inv.TaskID {
		return false, errors.New("finding 与调用记录的任务归属不一致")
	}
	task, err := s.m.pg.GetTask(inv.TaskID)
	if err != nil {
		return false, fmt.Errorf("读取任务失败: %w", err)
	}
	if task == nil {
		return false, errors.New("任务不存在")
	}
	target, err := s.m.pg.GetAgentByKey(inv.AgentKey)
	if err != nil {
		return false, fmt.Errorf("读取目标 Agent 失败: %w", err)
	}
	if target == nil {
		return false, errors.New("目标 Agent 不存在")
	}
	if !target.Enabled {
		return false, errors.New("目标 Agent 已禁用")
	}
	if target.Builtin {
		return false, errors.New("目标 Agent 不能是内置 Agent")
	}
	triggers, err := s.m.pg.ListTriggersFor(inv.AgentKey)
	if err != nil {
		return false, fmt.Errorf("读取目标 Agent 触发配置失败: %w", err)
	}
	triggerMessage := ""
	for _, tr := range triggers {
		if strings.TrimSpace(tr.FindingMessage) != "" {
			triggerMessage = tr.FindingMessage
			break
		}
	}
	triggerMessage = strings.NewReplacer(
		"__VULNCLASS__", finding.VulnClass,
		"__SEVERITY__", finding.Severity,
		"__SUMMARY__", finding.Summary,
	).Replace(triggerMessage)
	assetIDs, _ := json.Marshal(finding.AssetIDs)
	mode := agent.ExecutionModeFromGoal(task.Goal + "\n" + task.Description)
	var message strings.Builder
	if strings.TrimSpace(triggerMessage) != "" {
		message.WriteString(triggerMessage)
		message.WriteString("\n\n")
	}
	fmt.Fprintf(&message, "【本次由 planner 确认处置前提后显式派发】\nexecution_mode: %s\nFindingID: %d\nVulnClass: %s\nSeverity: %s\nSummary: %s\nAssetIDs: %s\nEvidenceVersion: %d\nPlannerReason: %s\nFindingEvidence: %s",
		mode, finding.ID, finding.VulnClass, finding.Severity, trunc(finding.Summary, 1000), assetIDs,
		finding.EvidenceVersion, trunc(inv.Reason, 2000), trunc(finding.Evidence, 12000))
	title := fmt.Sprintf("planner finding 派发 · task#%d · finding#%d", inv.TaskID, inv.FindingID)
	return s.startTriggeredRun(inv.AgentKey, title, message.String(), inv.TaskID, true, task.Description, task.Goal,
		[]findingInvocationRef{{agentKey: inv.AgentKey, findingID: inv.FindingID}}), nil
}
