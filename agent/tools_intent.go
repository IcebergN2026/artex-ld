package agent

// ═══ ARTEX 二开 · 审核流 PR4 ═══
// update_intent / cancel_intent 工具（§5/§9）：
//   - update_intent：修订 open 意图或被驳回（rejected）意图原稿；pending（审核中）一律
//     拒绝；带 expected_version 乐观锁与产出锁；驳回稿改后自动重送审核（review_status
//     复位 pending）并唤醒 review 轮。
//   - cancel_intent：软删除 open/proposed 意图（running/paused 拒绝并引导 control_work）。
// 调用方策略（§5.4）：planner 可改/可取消 open+rejected(proposed)；mainagent(worker="human")
// 只动 open；review 对话两个工具都挂 ask 确认卡（seedReviewIntercept 内置规则）；worker 不注册。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// intentIDVersionReq 是两个工具共用的必填参数：intent_id + expected_version。
// ExpectedVersion 用 *int 以区分「没填」（拒绝，强制模型先 list_intents 取版本）与「填 0」。
type intentIDVersionReq struct {
	IntentID        int64 `json:"intent_id"`
	ExpectedVersion *int  `json:"expected_version"`
}

func (r *intentIDVersionReq) validate() error {
	if r.IntentID <= 0 {
		return fmt.Errorf("intent_id 必填且必须为正整数")
	}
	if r.ExpectedVersion == nil {
		return fmt.Errorf("expected_version 必填：先调用 list_intents 查看该意图的 content_version 原样填入（乐观锁，防覆盖他人并发修改）")
	}
	return nil
}

// mainAgentOpenOnly mainagent（worker="human"）只允许动 open 意图（§5.4）：
// 驳回稿的修订权在 planner（revision_requested 事件驱动），人要走审核面板/对话。
// 前置预检（最终一致性仍由 db 层状态门禁+版本 CAS 保证）。
func (t *ToolSet) mainAgentOpenOnly(intentID int64) error {
	if t.worker != "human" {
		return nil
	}
	n, err := t.ts.GetNodeWithSources(intentID)
	if err != nil || n == nil {
		return fmt.Errorf("意图 #%d 不存在", intentID)
	}
	if n.State != "open" {
		return fmt.Errorf("mainagent 只能修改/取消 open 状态的意图（#%d 当前 state=%s）；待审/驳回稿请走审核面板，执行中意图请用 steer_work/control_work", intentID, n.State)
	}
	return nil
}

// updateIntent 修订一条意图（§5/§6）。
func (t *ToolSet) updateIntent() actool.CoreTool {
	return t.writeExpTool("update_intent",
		"【修订意图】修改一条 open（待执行）或 rejected（被驳回待修订）意图：可改 summary/priority/asset_ids/target_executor，未传字段保持不变。pending（审核中）与执行中意图不可修改。必填 expected_version（先用 list_intents 查 content_version 原样填入）。驳回稿有任何修改后自动重送审核（review_status→pending）；已有产出的意图不可再改（请 cancel_intent 后用 add_intent/add_review_intent 重发新稿）。",
		obj(map[string]any{
			"intent_id":        idp("要修订的意图 id"),
			"expected_version": intp("必填：该意图当前 content_version（list_intents 返回），乐观锁"),
			"summary":          str("可选：新的意图描述"),
			"priority":         intp("可选：新优先级 0-10"),
			"asset_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选：整组替换目标资产 id（资产锚点同步重建）"},
			"target_executor":  str("可选：新的建议执行器 executor_key（写 pending_executor，批准时转正；空串=清除建议）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var req struct {
				intentIDVersionReq
				Summary        *string  `json:"summary"`
				Priority       *int     `json:"priority"`
				AssetIDs       *[]int64 `json:"asset_ids"`
				TargetExecutor *string  `json:"target_executor"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			if err := req.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if req.Summary == nil && req.Priority == nil && req.AssetIDs == nil && req.TargetExecutor == nil {
				return actool.Errorf("至少要传一个待修改字段（summary/priority/asset_ids/target_executor）"), nil
			}
			if err := t.mainAgentOpenOnly(req.IntentID); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if req.TargetExecutor != nil && strings.TrimSpace(*req.TargetExecutor) != "" && t.checkExecutor != nil {
				te := strings.TrimSpace(*req.TargetExecutor)
				if err := t.checkExecutor(te); err != nil {
					return actool.Errorf(fmt.Sprintf("target_executor %q 不可用：%v；请改用已注册且启用的自定义 agent", te, err)), nil
				}
			}
			// 【审核-2 P1-4】修订 asset_ids 同样过资产范围拦截（与 createIntent 同口径）：
			// 否则范围外资产可经修订静默挂到意图锚点并交给 worker 执行。
			if req.AssetIDs != nil && len(*req.AssetIDs) > 0 && t.as != nil {
				if hits, err := t.as.CheckAssetsIntercept(t.taskID, *req.AssetIDs); err != nil {
					return actool.Errorf(fmt.Sprintf("资产拦截校验失败：%v", err)), nil
				} else if len(hits) > 0 {
					var b strings.Builder
					b.WriteString("意图修订绑定的资产未通过测试范围校验，请停止对相关资产进行测试：")
					for _, h := range hits {
						fmt.Fprintf(&b, "\n - %s", h.Describe())
					}
					return actool.Errorf(b.String()), nil
				}
			}
			upd, err := t.ts.UpdateIntent(req.IntentID, *req.ExpectedVersion, db.IntentPatch{
				Summary: req.Summary, Priority: req.Priority,
				AssetIDs: req.AssetIDs, TargetExecutor: req.TargetExecutor,
			}, t.worker)
			if err != nil {
				switch {
				case errors.Is(err, db.ErrStaleVersion):
					return actool.Errorf(fmt.Sprintf("版本冲突：%v；请重新 list_intents 取最新 content_version 再试", err)), nil
				case errors.Is(err, db.ErrIntentNotEditable), errors.Is(err, db.ErrIntentProduced),
					errors.Is(err, db.ErrNoChange), errors.Is(err, db.ErrIntentNotFound):
					return actool.Errorf(err.Error()), nil
				}
				return actool.Result{}, err
			}
			log.Printf("[intent] update_intent：意图 #%d 修订 %v（review_reset=%v，by %s）", upd.ID, upd.Changed, upd.ReviewReset, t.worker)
			if upd.ReviewReset && t.notifyReview != nil {
				t.notifyReview(upd.ID) // 驳回稿重送 → 立即唤醒审核轮（§2.6）
			}
			return jsonResult(upd)
		})
}

// cancelIntent 取消（软删除）一条意图（§9.1）。
func (t *ToolSet) cancelIntent() actool.CoreTool {
	return t.writeExpTool("cancel_intent",
		"【取消意图】软删除一条 open/proposed 意图（state→deleted，保留节点与血缘，理由记 delete_reason）：放弃某条待执行意图、放弃驳回稿的修订、或撤回误送审的稿子用它。必填 expected_version（list_intents 查 content_version）。running/paused 执行中意图不可取消——先用 control_work 停止执行。",
		obj(map[string]any{
			"intent_id":        idp("要取消的意图 id"),
			"expected_version": intp("必填：该意图当前 content_version（list_intents 返回），乐观锁"),
			"reason":           str("必填：取消理由（记 delete_reason，审计可见）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var req struct {
				intentIDVersionReq
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			if err := req.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := t.mainAgentOpenOnly(req.IntentID); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 【审核-2 P2-8】reason 必填（§11 PR4 规格）——取消是破坏性动作，理由必须可追溯；
			// db 侧的默认理由只作兜底，不再作为主要路径。
			if strings.TrimSpace(req.Reason) == "" {
				return actool.Errorf("reason 必填：取消理由（记 delete_reason，审计可见）"), nil
			}
			summary, err := t.ts.SoftDeleteIntentAt(req.IntentID, strings.TrimSpace(req.Reason), *req.ExpectedVersion)
			if err != nil {
				switch {
				case errors.Is(err, db.ErrStaleVersion):
					return actool.Errorf(fmt.Sprintf("版本冲突：%v；请重新 list_intents 取最新 content_version 再试", err)), nil
				case errors.Is(err, db.ErrIntentNotEditable), errors.Is(err, db.ErrIntentNotFound):
					return actool.Errorf(err.Error()), nil
				}
				return actool.Result{}, err
			}
			log.Printf("[intent] cancel_intent：意图 #%d 已软删（by %s，理由=%q）", req.IntentID, t.worker, req.Reason)
			if t.notifyHint != nil {
				// 取消改变 frontier 格局 → 唤醒 planner 重审目标达成判定。
				t.notifyHint([]string{fmt.Sprintf("意图 #%d（%s）已取消：%s", req.IntentID, firstLine(summary, 60), req.Reason)})
			} else if t.notify != nil {
				t.notify()
			}
			return jsonResult(map[string]any{
				"intent_id": req.IntentID, "state": "deleted", "summary": summary,
			})
		})
}
