package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func findingHandoffMarker(findingID int64) string {
	return fmt.Sprintf("[dispatcher_finding_id=%d]", findingID)
}

func clipFindingHandoff(text string, maxRunes int) string {
	r := []rune(strings.TrimSpace(text))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes]) + "…"
}

func findingHandoffText(f *db.DBFinding) string {
	var b strings.Builder
	marker := findingHandoffMarker(f.ID)
	b.WriteString(marker)
	b.WriteString("\n同一资产新增 Finding，请纳入当前内网渗透规划，不要重复创建子任务。\n")
	if f.TaskID != nil {
		fmt.Fprintf(&b, "来源任务：#%d\n", *f.TaskID)
	}
	fmt.Fprintf(&b, "Finding：#%d · %s · %s\n", f.ID, f.VulnClass, f.Severity)
	if name := clipFindingHandoff(f.Name, 500); name != "" {
		b.WriteString("名称：" + name + "\n")
	}
	b.WriteString("摘要：" + clipFindingHandoff(f.Summary, 4000) + "\n")
	if evidence := clipFindingHandoff(f.Evidence, 8000); evidence != "" {
		b.WriteString("证据：\n" + evidence)
	}
	return b.String()
}

// toolAppendFindingToTask supplements the internal-pentest task already
// responsible for a finding's asset. A persisted marker makes retries
// idempotent; hints wake a running planner, while terminal tasks receive a new
// goal before shared task admission revives them.
func (s *Server) toolAppendFindingToTask() actool.CoreTool {
	return wrTool("append_finding_to_task",
		"把 Finding 的内容和证据补充到负责同一资产的内网渗透任务。参数 task_id=内网任务 id，finding_id=Finding id；多主机 Finding 必须传 target_asset。允许跨父子血缘，但目标必须登记相同资产；终态任务会新增目标并复活，重复追加返回 already_appended。",
		objSchema(map[string]any{
			"task_id":      strParam("负责同一资产的内网渗透任务 id"),
			"finding_id":   map[string]any{"type": "integer", "description": "要补充的 Finding id"},
			"target_asset": strParam("可选：多主机 Finding 的精确资产 IP/域名/URL"),
		}, "task_id", "finding_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID      string          `json:"task_id"`
				FindingID   json.RawMessage `json:"finding_id"`
				TargetAsset string          `json:"target_asset"`
			}
			_ = json.Unmarshal(in, &a)
			taskID := strings.TrimSpace(a.TaskID)
			findingID := parseProfileID(a.FindingID)
			if taskID == "" || findingID <= 0 {
				return actool.Errorf("task_id 和有效的 finding_id 为必填"), nil
			}
			target, ok := s.m.Task(taskID)
			if !ok || target == nil {
				return actool.Errorf("子任务不存在: " + taskID), nil
			}
			finding, err := s.m.pg.GetFinding(findingID)
			if err != nil {
				return actool.Errorf("读取 Finding 失败: " + err.Error()), nil
			}
			if finding == nil {
				return actool.Errorf("Finding 不存在"), nil
			}
			if target.CategoryName != "内网渗透" {
				return actool.Errorf("目标任务不是内网渗透子任务"), nil
			}
			assetKeys, err := s.findingDispatchAssetKeys(findingID, a.TargetAsset)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("解析 Finding 资产失败: %v", err)), nil
			}
			matchedAsset := ""
			for _, key := range assetKeys {
				var exists bool
				if err := s.m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_dispatch_targets
					WHERE task_id=$1 AND asset_key=$2)`, target.ID, key).Scan(&exists); err != nil {
					return actool.Errorf(fmt.Sprintf("检查目标任务资产失败: %v", err)), nil
				}
				if exists {
					matchedAsset = key
					break
				}
			}
			if matchedAsset == "" {
				return actool.Errorf("目标任务未登记该 Finding 的资产，不能追加"), nil
			}

			marker := findingHandoffMarker(findingID)
			reviveTarget := func() (bool, error) {
				lifecycle := target.lifecycleSnapshot()
				if lifecycle.Status == "running" && !lifecycle.Paused && !lifecycle.Queued {
					return false, nil
				}
				_, err := s.admitTask(target, "resume")
				return err == nil, err
			}
			var existingHintID int64
			err = s.m.pg.QueryRow(`SELECT id FROM exploration_nodes
				WHERE exploration_id=$1 AND kind='hint' AND payload->>'text' LIKE $2
				ORDER BY id LIMIT 1`, target.ExpID, marker+"%").Scan(&existingHintID)
			if err == nil {
				revived, reviveErr := reviveTarget()
				if reviveErr != nil {
					return actool.Errorf(fmt.Sprintf("Finding 已追加，但任务恢复失败: %v", reviveErr)), nil
				}
				return jsonResult(map[string]any{
					"status": "already_appended", "task_id": taskID,
					"finding_id": findingID, "hint_id": existingHintID, "revived": revived,
				})
			}
			if err != sql.ErrNoRows {
				return actool.Errorf("检查 Finding 追加记录失败: " + err.Error()), nil
			}

			// A terminal task with no open goal would swallow a plain hint in the
			// goalless engine branch. Add an idempotent follow-up goal before revive.
			var goalID int64
			if isTerminalStatus(target.lifecycleSnapshot().Status) {
				openGoal, goalErr := target.Store.HasOpenGoal()
				if goalErr != nil {
					return actool.Errorf(fmt.Sprintf("检查目标任务失败: %v", goalErr)), nil
				}
				if !openGoal {
					goalText := fmt.Sprintf("%s 评估并处理同一资产新增 Finding #%d，纳入内网渗透规划。", marker, findingID)
					var goalState string
					goalErr = s.m.pg.QueryRow(`SELECT id,state FROM exploration_nodes
						WHERE exploration_id=$1 AND kind='goal' AND payload->>'text' LIKE $2
						ORDER BY id DESC LIMIT 1`, target.ExpID, marker+"%").Scan(&goalID, &goalState)
					switch goalErr {
					case nil:
						if goalState != "open" {
							if _, goalErr = s.m.pg.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, goalID); goalErr != nil {
								return actool.Errorf(fmt.Sprintf("重新打开 Finding 目标失败: %v", goalErr)), nil
							}
							target.NotifyGoal([]string{goalText})
						}
					case sql.ErrNoRows:
						goalID, goalErr = target.Store.AddGoal(map[string]any{"text": goalText, "vulnclass": finding.VulnClass}, "vuln_dispatcher")
						if goalErr != nil {
							return actool.Errorf(fmt.Sprintf("新增 Finding 目标失败: %v", goalErr)), nil
						}
						if of, _ := target.Store.OriginFactID(); of > 0 {
							_ = target.Store.Link(of, db.RelSpawns, goalID)
						}
						target.NotifyGoal([]string{goalText})
					default:
						return actool.Errorf(fmt.Sprintf("读取 Finding 目标失败: %v", goalErr)), nil
					}
				}
			}
			revived, reviveErr := reviveTarget()
			if reviveErr != nil {
				return actool.Errorf(fmt.Sprintf("恢复内网任务失败: %v", reviveErr)), nil
			}

			text := findingHandoffText(finding)
			payload, _ := json.Marshal(map[string]any{
				"task_id": taskID, "text": text, "asset_ids": finding.AssetIDs,
			})
			res, err := s.delegateToTask(ctx, payload, (*agent.ToolSet).AddHintTool)
			if err != nil || res.IsError {
				return res, err
			}
			var hintID int64
			_, _ = fmt.Sscanf(res.Flatten(), "hint added: %d", &hintID)
			auditID, auditErr := target.Store.AppendActivity(db.Activity{
				NodeID: &hintID, Worker: "vuln_dispatcher", Kind: "finding_handoff",
				Summary: fmt.Sprintf("Finding #%d 已补充到同资产内网子任务", findingID),
				Detail:  fmt.Sprintf("task_id=%s finding_id=%d hint_id=%d goal_id=%d asset=%s revived=%t", taskID, findingID, hintID, goalID, matchedAsset, revived),
			})
			if auditErr != nil {
				log.Printf("[dispatcher] Finding 补充审计记录失败 task=%s finding=%d: %v", taskID, findingID, auditErr)
			}
			return jsonResult(map[string]any{
				"status": "appended", "task_id": taskID, "finding_id": findingID,
				"hint_id": hintID, "audit_id": auditID, "goal_id": goalID,
				"asset_key": matchedAsset, "revived": revived,
			})
		})
}
