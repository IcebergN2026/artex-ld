// ═══ ARTEX 二开 · [P-REVIEW·A3 审核工具组] ═══
// 功能介绍：review agent 的四个图工具——list_intents（读，K4=取代原 get_proposed_intents，m03206）/set_intent_status（处置）/
//
//	send_planner_hint（优化点下发）/add_review_intent（送审唯一入口+优化版替代）；
//	双模式工具子集：预审轮物理只读（K14），会话轮写动作全走 intercept ask（K15）
//
// 二开日期：2026-10-21
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log" // 【二开·P-LOG·A3】工具被调 DEBUG/INFO
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	agentcore "github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// ── 安全护栏（对应原 D-b 红线，用户拍板要此工具组，故以工程护栏替代"不给"）──
// 1. set_intent_status 只出现在 review agent 的 ToolSet（经 ReviewTools 取值），
//    worker/planner/mainagent/ChatAgent 物理上没有这个工具；
// 2. 处置白名单只有 approve（proposed→open）与 reject（保持 proposed、review_status→rejected
//    退回修订）两条，LLM 无法用它把 done/blocked 翻案、无法自审自批别种状态（其余一律报错）；
// 3. 全部走版本化 CAS（ApproveIntentAt/RequestRevisionAt，携带 expected_version），
//    与人工 UI 并发安全——后到者 rows=0 → 返回"已被处理"，先到先得；
// 4. 写工具 intercept ask 化（D9）：agent 一调就挂起等人工按钮，授权由机制保证
//    而非提示词纪律（K15）。

// ReviewReadOnlyTools 预审轮（reviewRunner，无人在场）工具子集——物理只读（K14）：
// 只读图工具 + list_intents（K4=取代 get_proposed_intents：全状态白名单+游标分页+关键词过滤），
// 没有任何写工具，LLM 想批也批不了。
func (t *ToolSet) ReviewReadOnlyTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.GraphOverviewTool(),
		t.ListFindingsTool(),
		t.NodeDetailTool(),
		t.listIntents(), // 【二开·K4】取代 get_proposed_intents（待审视图=states=["proposed"]）
	}
}

// ReviewTools 会话轮（人在场对话）全工具组：只读子集 + 五个写工具
// （全部已被 D9 的 [内置] ask 规则覆盖，调用即挂起等人点确认卡）。
func (t *ToolSet) ReviewTools() []actool.CoreTool {
	return append(t.ReviewReadOnlyTools(),
		t.setIntentStatus(),
		t.sendPlannerHint(),
		t.addReviewIntent(),
		// 【二开·审核流 PR4/§5.4】review 对话可修订/取消，但全部挂 ask 确认卡；
		// pending（审核中）稿的修改由 db 层状态门禁一律拒绝。
		t.updateIntent(),
		t.cancelIntent(),
	)
}

// getProposedIntents 列出当前任务待审意图（id/summary/priority/pending_executor/review_note/replaces；
// D3 ProposedIntents 已按 m00973/K24 双条件过滤，仅 executor='review' 的审核模式意图）。
// 【二开·K4】get_proposed_intents 已废除：由通用 list_intents（states=["proposed"]）取代（m03206 拍板）。
// 如需撤销本取代，从 git 历史恢复此函数并把 ReviewReadOnlyTools 的 t.listIntents() 换回。

// setIntentStatus 批准/驳回一条待审意图（会话轮写工具，D9 ask 规则挂起等人确认）。
// 【二开·审核流 PR3/PR5】全部走版本化 CAS：approve=ApproveIntentAt（执行器由
// pending_executor 转正，不再接受外部 executor 参数）；reject=RequestRevisionAt
// （退回修订：state 保持 proposed、review_status='rejected'，不再是软删），成功后经
// notifyRevision 发 revision_requested 触发（§4.2.3 三件套）。
func (t *ToolSet) setIntentStatus() actool.CoreTool {
	return t.writeExpTool("set_intent_status",
		"批准或驳回一条待审（proposed+executor=review+review_status=pending）意图。action=approve 批准入池（执行器=送审时预选的 pending_executor 自动转正，未预选则由引擎默认路由）；action=reject 驳回退回修订（reason 必填；意图保持 proposed 并标记 review_status=rejected，planner 会用 update_intent 修订后自动重送审）。只对审核模式意图生效。",
		obj(map[string]any{
			"intent_id": idp("待审意图 id"),
			"action":    str("approve 或 reject"),
			"reason":    str("驳回理由（action=reject 时必填）"),
		}, "intent_id", "action"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var req struct {
				IntentID int64  `json:"intent_id"`
				Action   string `json:"action"`
				Reason   string `json:"reason"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			if req.IntentID <= 0 {
				return actool.Errorf("intent_id required"), nil
			}
			// 先读当前节点拿 content_version 作乐观锁版本（审的就是这份稿）。
			node, err := t.ts.GetNode(req.IntentID)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("读取意图失败: %v", err)), nil
			}
			if node == nil {
				return actool.Errorf(fmt.Sprintf("意图 #%d 不存在", req.IntentID)), nil
			}
			switch req.Action {
			case "approve":
				ok, err := t.ts.ApproveIntentAt(req.IntentID, node.ContentVersion)
				if err != nil {
					return actool.Errorf(fmt.Sprintf("approve failed: %v", reviewErrText(err, req.IntentID))), nil
				}
				if !ok {
					log.Printf("[review] set_intent_status approve 意图 #%d 未生效（并发冲突）", req.IntentID)
					return actool.Errorf(fmt.Sprintf("意图 #%d 不在待审状态（可能已被处理）", req.IntentID)), nil
				}
				log.Printf("[review] review 批准意图 #%d（proposed → open，pending_executor 转正）", req.IntentID)
				if t.notify != nil {
					t.notify() // 【二开·审核流 PR1/§2.6.1】批准放行 → 唤醒 planner/worker 立即领取
				}
				return jsonResult(map[string]any{"approved": req.IntentID})
			case "reject":
				if strings.TrimSpace(req.Reason) == "" {
					return actool.Errorf("action=reject 时 reason 必填"), nil
				}
				_, newVersion, ok, err := t.ts.RequestRevisionAt(req.IntentID, req.Reason, node.ContentVersion)
				if err != nil {
					return actool.Errorf(fmt.Sprintf("reject failed: %v", reviewErrText(err, req.IntentID))), nil
				}
				if !ok {
					log.Printf("[review] set_intent_status reject 意图 #%d 未生效（并发冲突）", req.IntentID)
					return actool.Errorf(fmt.Sprintf("意图 #%d 不在待审状态（可能已被处理）", req.IntentID)), nil
				}
				log.Printf("[review] review 驳回意图 #%d（proposed 保持，review_status=rejected，理由：%s）", req.IntentID, req.Reason)
				// §4.2.3 三件套：revision_requested 事件（带 IntentID+理由），不用 hint 伪装。
				if t.notifyRevision != nil {
					t.notifyRevision(req.IntentID, req.Reason)
				} else if t.notify != nil {
					t.notify()
				}
				return jsonResult(map[string]any{
					"intent_id": req.IntentID, "review_status": "rejected",
					"content_version": newVersion,
					"note":            "已退回 planner 修订：请等待修订稿重新送审",
				})
			default:
				return actool.Errorf("action 只能是 approve 或 reject"), nil
			}
		})
}

// reviewErrText 把 db 层哨兵错误翻成面向调用方的中文（404/409/422 语义）。
func reviewErrText(err error, id int64) error {
	switch {
	case errors.Is(err, db.ErrIntentNotFound):
		return fmt.Errorf("意图 #%d 不存在", id)
	case errors.Is(err, db.ErrNotPendingReview):
		return fmt.Errorf("意图 #%d 不在待审状态（可能已被处理）", id)
	case errors.Is(err, db.ErrStaleVersion):
		return fmt.Errorf("意图 #%d 在处置期间被修改，请刷新后重试", id)
	case errors.Is(err, db.ErrNoteRequired):
		return fmt.Errorf("驳回理由必填")
	default:
		return err
	}
}

// sendPlannerHint 给 planner 下发审核意见（K16 路线 B：不直产替代，让 planner 重产）。
// 复用 addOneHint 落库（origin="review"），经 notifyHint 唤醒 planner 下一轮。
func (t *ToolSet) sendPlannerHint() actool.CoreTool {
	return t.writeExpTool("send_planner_hint",
		"给规划 planner 下发一条审核意见/调整建议（hint），planner 下一轮规划时会看到并据此重产意图。用于「驳回后希望 planner 换个方向重产」的场景。",
		obj(map[string]any{
			"hint": str("给 planner 的审核意见/调整建议（必填）"),
		}, "hint"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var req struct {
				Hint string `json:"hint"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			if req.Hint == "" {
				return actool.Errorf("hint required"), nil
			}
			id, err := t.ts.AddNode("hint", map[string]any{"text": req.Hint}, 0, "active", "review", nil)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("hint failed: %v", err)), nil
			}
			if t.notifyHint != nil {
				t.notifyHint([]string{req.Hint})
			} else if t.notify != nil {
				t.notify()
			}
			return jsonResult(map[string]any{"hint_id": id})
		})
}

// addReviewIntent 送审唯一入口（m00339，ARTEX 二开·审核流 PR1 重写）：任何 agent 想把意图送入
// 人工审核，只能调它——落 proposed 且强制 review_status='pending' + executor='review' 占位
// （§2.9.5 闸门双条件）。与 add_intent（直投 open 立即入池）严格分工：本工具只送审、不执行。
// 资产与血缘走公共内核 createIntent（§2.3）：asset_ids → exploration_anchors + payload.asset_ids；
// parent_ids → 只建 derived_from 血缘（E1 修复：不再被误当资产锚点）。
// target_executor（建议执行器）送审时经 checkExecutor 校验（§2.9.3），存 payload.pending_executor，
// 批准时转正；executor 不是参数（§2.9.1），想改派执行器只能驳回→update_intent→重送。
// K16 路线 A 由 review agent 用它直产优化版替代意图（payload.replaces=旧意图 id，K21，带存在性校验）。
// review_enabled=false 时明确报错（§2.4：不得静默改投 open）。送审成功经 wakeReview 立即唤醒
// ReviewRunner（§2.6：单个/批量都收口，不再只靠 60s 轮询）。
func (t *ToolSet) addReviewIntent() actool.CoreTool {
	return t.writeExpTool("add_review_intent",
		"【送审】提交一条意图进入人工审核：落 proposed + review_status=pending + executor='review' 占位，不会直接执行，只被审核面板/审核轮拾取；审核通过后同一节点转 open（资产与血缘保持不变）。与 add_intent（直投 open、立即执行）不同，本工具只送审。支持单条或 intents 数组批量。",
		obj(map[string]any{
			"summary":         str("意图描述（单条模式）：做什么+为什么"),
			"priority":        intp("优先级 1-10，默认 5"),
			"asset_ids":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选：目标资产 id（list_assets 返回的资产 id，不是探索节点 id）→ 资产锚点 + payload.asset_ids"},
			"parent_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选：上游锚点 id（只能填已存在的 fact/finding 节点 id），只建 derived_from 血缘，不产生资产锚点；顶层全新方向留空（自动挂 origin fact）"},
			"replaces":        idp("可选：本条意图替代的旧意图 id（必须已存在）"),
			"target_executor": str("可选：建议执行器标识（executor_key，必须是已注册且启用的自定义 agent）→ payload.pending_executor，审核通过时自动转正为正式 executor"),
			"reason":          str("可选：送审理由/给审核人的说明（写入 payload.review_reason 供审核参考）"),
			"intents":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "批量模式：每条 {summary,priority?,asset_ids?,parent_ids?,replaces?,target_executor?,reason?}。返回 ids 与本数组等长同序（失败项 id=0，详情见 errors）"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			// §2.4：审核总开关在落库前即时校验（不是一轮开始时的快照）；false 明确报错，
			// 绝不静默改调 add_intent（否则模型以为已送审，实际绕过审核直接执行）。
			if t.reviewEnabledFn != nil && !t.reviewEnabledFn() {
				return actool.Errorf("该任务未开启意图审核（review_enabled=false），add_review_intent 不可用；确认无需审核时请改用 add_intent 直投"), nil
			}
			var req struct {
				intentItem
				Replaces *int64            `json:"replaces"`
				Reason   string            `json:"reason"`
				Intents  []json.RawMessage `json:"intents"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			type reviewItem struct {
				intentItem
				Replaces *int64 `json:"replaces"`
				Reason   string `json:"reason"`
			}
			addOne := func(it reviewItem) (int64, error) {
				// extraPayload 白名单组装（§2.4：只允许服务端校验过的字段）。
				extra := map[string]any{}
				if strings.TrimSpace(it.TargetExecutor) != "" {
					it.TargetExecutor = strings.TrimSpace(it.TargetExecutor)
					// §2.9.3：送审时就校验建议执行器——批准时转正，若不合法，批准后会静默
					// 回退默认 worker，审核结论与实际执行不一致且审计不可见。
					if t.checkExecutor != nil {
						if err := t.checkExecutor(it.TargetExecutor); err != nil {
							return 0, fmt.Errorf("target_executor %q 不可用：%v；请改用已注册且启用的自定义 agent，或不传该参数", it.TargetExecutor, err)
						}
					}
					extra["pending_executor"] = it.TargetExecutor
				}
				if it.Replaces != nil && *it.Replaces > 0 {
					// §2.9.2：replaces 增加存在性校验（只记录关系，不影响调度）。
					if n, err := t.ts.GetNodeWithSources(*it.Replaces); err != nil || n == nil {
						return 0, fmt.Errorf("replaces 旧意图 #%d 不存在", *it.Replaces)
					}
					extra["replaces"] = *it.Replaces // K21：替代关系落 payload 一个 int64
				}
				if strings.TrimSpace(it.Reason) != "" {
					extra["review_reason"] = strings.TrimSpace(it.Reason)
				}
				return t.createIntent(it.intentItem, true, "", extra)
			}
			// 批量模式
			if len(req.Intents) > 0 {
				ids := make([]int64, len(req.Intents))
				errs := map[string]string{}
				createdAny := false
				for i, raw := range req.Intents {
					var it reviewItem
					if err := json.Unmarshal(raw, &it); err != nil {
						errs[strconv.Itoa(i)] = err.Error()
						continue
					}
					id, err := addOne(it)
					if err != nil {
						errs[strconv.Itoa(i)] = err.Error()
						continue
					}
					ids[i] = id
					createdAny = true
				}
				// 【二开·P-LOG·A3】INFO 记批量送审。
				log.Printf("[review] add_review_intent 批量送审 %d 条（失败 %d）", len(ids)-len(errs), len(errs))
				if createdAny {
					t.wakeReview() // §2.6：批量合并为一次唤醒（收口在 wakeReview）
				}
				out := map[string]any{"ids": ids, "proposed": true}
				if len(errs) > 0 {
					out["errors"] = errs
				}
				return jsonResult(out)
			}
			// 单条模式
			id, err := addOne(reviewItem{intentItem: req.intentItem, Replaces: req.Replaces, Reason: req.Reason})
			if err != nil {
				return actool.Errorf(fmt.Sprintf("%v", err)), nil
			}
			t.wakeReview() // §2.6：送审成功立即唤醒 ReviewRunner（ticker 仍是兜底）
			// 【二开·P-LOG·A3】INFO 记送审（排障：这条 proposed 哪来的）。
			log.Printf("[review] add_review_intent 送审意图 #%d（origin=%s）", id, t.worker)
			return jsonResult(map[string]any{"created": true, "proposed": true, "intent_id": id})
		})
}

// ═══ ARTEX 二开 · [P-AGENT·S5 预审装配] ═══
// 功能介绍：RunReviewPreScreen——只读预审一轮，产出 {意图id: {verdict, note}}（§3.3 契约）
// 二开日期：2026-10-21

// PreScreenVerdict 预审单条建议（§3.3）：pass=建议放行；revise=建议修订。两种结论都只落标注，意图保持待审。
type PreScreenVerdict struct {
	Verdict string // pass | revise
	Note    string // revise 时必填（需要优化的内容，server 截断 ≤200 字）
}

// ReviewPreScreenConfig 预审输入。Provider 由 server 侧解析注入（S5 落地注②）。
type ReviewPreScreenConfig struct {
	Store    *db.ExplorationStore
	Goal     string
	TaskID   string
	Provider llm.Provider
	FreshIDs []int64 // 本次需要出预审意见的意图 id（fresh=未审过或审后正文又变过，§3.4）
	Emit     func(db.Activity)
}

// RunReviewPreScreen 只读预审一轮：review 提示词 + ReviewReadOnlyTools（K14 物理只读），
// 让 LLM 逐条看 FreshIDs 的意图并输出裁决 JSON。
// 【二开·审核流 PR6/§3.3】输出契约从 {id: 摘要} 改为 {id: {verdict: pass|revise, note}}：
// 人工全审下 pass / revise 均只写预审建议，不调用 ApproveIntentAt；由人工在面板或审核对话中最终处置。
// id 严格解析（#123 / "123" 两种）且只认 FreshIDs 白名单——幻觉出的意图号整条丢弃。
func RunReviewPreScreen(ctx context.Context, cfg ReviewPreScreenConfig) (map[int64]PreScreenVerdict, error) {
	ts := NewToolSet(cfg.Store, "review")
	tools := ts.ReviewReadOnlyTools() // K14：物理只读子集，写工具缺席
	sysBody := renderSystem("review", reviewDefaultTmpl, map[string]any{
		"Goal": cfg.Goal, "Now": time.Now().Format("2006-01-02 15:04"),
	})
	var ids []string
	for _, id := range cfg.FreshIDs {
		ids = append(ids, fmt.Sprintf("#%d", id))
	}
	input := fmt.Sprintf("以下是本轮需要预审的待审意图 id：%s。\n请用 list_intents(states=[\"proposed\"]) 拉取详情（审核模式下 proposed 即待审意图），结合 graph_overview 看任务全貌，逐条给出裁决，最后只输出 JSON：\n{\"<id>\": {\"verdict\": \"pass|revise\", \"note\": \"<≤200字>\"}, ...}\n- pass：与任务目标相关、不重复、不危险、执行器适配（按送审时预选的 pending_executor 判断，无预选=默认 worker），建议放行（note 可留空或写一句通过理由；仅标注，不会自动批准）；\n- revise：不建议放行，note 必填——写清需要优化的内容（给人工审核聚焦用）。",
		strings.Join(ids, "、"))
	out, _, err := captureRun(ctx, agentcore.Options{
		Provider:       cfg.Provider,
		SystemPrompt:   []string{sysBody},
		Tools:          tools,
		PermissionMode: permission.ModeBypass,
		MaxTurns:       20,
		MaxDuration:    5 * time.Minute,
	}, input, cfg.Emit)
	if err != nil {
		return nil, err
	}
	// 解析 LLM 输出的 JSON 映射（宽松解析：截取首尾花括号段）
	lo, hi := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if lo < 0 || hi <= lo {
		return nil, fmt.Errorf("预审输出不含 JSON 映射")
	}
	var raw map[string]struct {
		Verdict string `json:"verdict"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out[lo:hi+1]), &raw); err != nil {
		return nil, fmt.Errorf("预审 JSON 解析失败: %w", err)
	}
	fresh := map[int64]bool{}
	for _, id := range cfg.FreshIDs {
		fresh[id] = true
	}
	res := make(map[int64]PreScreenVerdict, len(raw))
	for k, v := range raw {
		id, ok := parseReviewID(k)
		if !ok || !fresh[id] {
			continue // 白名单：只认本轮 fresh 集合内的 id（防幻觉意图号）
		}
		verdict := strings.ToLower(strings.TrimSpace(v.Verdict))
		if verdict != "pass" && verdict != "revise" {
			continue // 非法 verdict 整条丢弃（§3.3：宁缺勿错）
		}
		res[id] = PreScreenVerdict{Verdict: verdict, Note: strings.TrimSpace(v.Note)}
	}
	return res, nil
}

// parseReviewID 严格解析审核输出里的意图 id：接受 "123" / "#123" 两种形态，
// 拒绝一切非纯数字（防 "#123, #124" 之类合并串被 Sscanf 截断误收）。
func parseReviewID(s string) (int64, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// ═══ ARTEX 二开 · [P-AGENT·S6 会话轮装配] ═══
// 功能介绍：RunReviewChat——人在场审核对话轮，挂 ReviewTools 全组（写工具由 intercept ask 挂起，K15）
// 二开日期：2026-10-21

// ReviewChatConfig 会话轮输入。Hooks 传任务 Guard.Hooks()（D9 ask 规则在此生效）。
type ReviewChatConfig struct {
	Store          *db.ExplorationStore
	Goal           string
	TaskID         string
	Provider       llm.Provider
	Message        string
	Hooks          harness.HookRunner // 任务 Guard 的 hook 注册表（intercept ask 挂起点）
	Emit           func(db.Activity)
	Notify         func() // 【二开·审核流 PR1】planner/worker 唤醒（批准放行后领取）
	NotifyHint     func([]string)
	NotifyReview   func(taskID int64)                  // 【二开·审核流 PR1/§2.6】ReviewRunner 专用唤醒
	NotifyRevision func(intentID int64, reason string) // 【二开·审核流 PR5/§4.2.3】revision_requested 触发
	// 【二开·审核流 PR8/审核-2 P1】target_executor 校验器：review 对话里送审/修订也要
	// 校验建议执行器（否则非法 pending_executor 落库后批准时无条件转正）。
	CheckExecutor func(key string) error
}

// RunReviewChat 跑一轮审核对话：review 提示词 + ReviewTools 全组。
// 写工具（set_intent_status/add_review_intent/send_planner_hint/update_intent/cancel_intent）
// 已挂 D9 ask 规则，LLM 一调用就被 intercept 挂起，直到人在 ReviewPanel 确认卡点按钮才继续（K15）。
func RunReviewChat(ctx context.Context, cfg ReviewChatConfig) error {
	ts := NewToolSet(cfg.Store, "review")
	ts.SetNotify(cfg.Notify)
	ts.SetNotifyHint(cfg.NotifyHint)
	ts.SetNotifyReview(cfg.NotifyReview)
	ts.SetNotifyRevision(cfg.NotifyRevision)
	ts.SetExecutorChecker(cfg.CheckExecutor)
	tools := ts.ReviewTools() // 只读子集 + 五个写工具（写工具走 ask）
	// 【二开·审核流 PR1/§2.8】绑定任务上下文（agentName="review"）：K15 审核写闸门
	// 靠它识别调用来源——只有 review 会话的三件写工具才弹人工确认卡，planner 送审
	// 等同名调用由 guard 侧 SkipReviewGateFor 绕过（不进规则匹配/LLM 判官）。
	ctx = intercept.WithTaskContext(ctx, cfg.TaskID, "review", cfg.Emit)
	sysBody := renderSystem("review", reviewDefaultTmpl, map[string]any{
		"Goal": cfg.Goal, "Now": time.Now().Format("2006-01-02 15:04"),
	})
	_, _, err := captureRun(ctx, agentcore.Options{
		Provider:       cfg.Provider,
		SystemPrompt:   []string{sysBody},
		Tools:          tools,
		Hooks:          cfg.Hooks,
		PermissionMode: permission.ModeBypass,
		MaxTurns:       30,
		MaxDuration:    10 * time.Minute,
	}, cfg.Message, cfg.Emit)
	return err
}

// ═══ ARTEX 二开 · [P-REVIEW·S5b 自动审核] ═══
// 功能介绍：RunReviewAuto——任务开了「自动处置」（tasks.review_auto，审核聊天窗 Switch）后，
// ReviewRunner 预审轮走这里：LLM 只读研判 + 输出决策 JSON，server 端解析后经 D3 CAS 执行。
// 关键设计：LLM 自始至终不碰写工具（工具组仍是 ReviewReadOnlyTools），决策以数据形式返回，
// 由 server 执行 ApproveIntentAt/RequestRevisionAt——拦截 ask 规则零冲突，格式强约束，留痕完整。
// 二开日期：2026-10-22

// ReviewDecision 自动审核单条处置决定。
// 【二开·审核流 PR6/§3.5】收窄为 approve/reject 两态：拿不准=不输出该 id（未决），
// 不再 skip 也不落研判批注；未决不做任何写操作。执行器不开放指定——批准时
// pending_executor 自动转正（§8）。
type ReviewDecision struct {
	ID     int64  // 意图 id
	Action string // approve=放行 | reject=驳回退回修订
	Note   string // 处置理由（server 截断 ≤200 字，落 review_note/留痕）
}

// ReviewAutoConfig 自动审核一轮的输入。
type ReviewAutoConfig struct {
	Store    *db.ExplorationStore
	Goal     string
	TaskID   string
	Provider llm.Provider
	FreshIDs []int64
	Emit     func(db.Activity)
}

// RunReviewAuto 自动处置一轮：只读工具研判 + 决策 JSON 输出。
// 返回的 decisions 由调用方（ReviewRunner）执行；解析失败/LLM 出错 = 本轮全部留人（安全兜底）。
func RunReviewAuto(ctx context.Context, cfg ReviewAutoConfig) ([]ReviewDecision, error) {
	ts := NewToolSet(cfg.Store, "review")
	tools := ts.ReviewReadOnlyTools() // 仍然物理只读：决策走 JSON 输出，不走写工具
	sysBody := renderSystem("review", reviewDefaultTmpl, map[string]any{
		"Goal": cfg.Goal, "Now": time.Now().Format("2006-01-02 15:04"),
	})
	var ids []string
	for _, id := range cfg.FreshIDs {
		ids = append(ids, fmt.Sprintf("#%d", id))
	}
	input := fmt.Sprintf(`以下是本轮待审意图 id：%s。
请用 list_intents(states=[\"proposed\"]) 拉取详情（审核模式下 proposed 即待审意图），结合 graph_overview 看任务全貌，然后对你能明确判断的每一条给出处置决定：
- approve：明显安全且与任务目标强相关；
- reject：明显重复或明显危险（删改业务数据、DoS、关闭审计、持久化后门等，note 必填，写清需要优化的内容——会退回 planner 修订原稿后重新送审）；
- 拿不准的【不要输出】该 id：未决不做任何写操作，留给人工。
每个决定附 ≤200 字理由 note。
最后只输出 JSON：{"<id>": {"action": "approve|reject", "note": "<理由>"}, ...}`,
		strings.Join(ids, "、"))
	out, _, err := captureRun(ctx, agentcore.Options{
		Provider:       cfg.Provider,
		SystemPrompt:   []string{sysBody},
		Tools:          tools,
		PermissionMode: permission.ModeBypass,
		MaxTurns:       20,
		MaxDuration:    5 * time.Minute,
	}, input, cfg.Emit)
	if err != nil {
		return nil, err
	}
	lo, hi := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if lo < 0 || hi <= lo {
		return nil, fmt.Errorf("自动审核输出不含 JSON 决策映射")
	}
	var raw map[string]struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out[lo:hi+1]), &raw); err != nil {
		return nil, fmt.Errorf("自动审核决策 JSON 解析失败: %w", err)
	}
	fresh := map[int64]bool{}
	for _, id := range cfg.FreshIDs {
		fresh[id] = true
	}
	var out2 []ReviewDecision
	for idStr, d := range raw {
		id, ok := parseReviewID(idStr)
		if !ok || !fresh[id] {
			continue // 只认本轮 fresh 集合内的 id——防 LLM 幻觉出别的意图号
		}
		action := strings.ToLower(strings.TrimSpace(d.Action))
		if action != "approve" && action != "reject" {
			continue // 【二开·审核流 PR6/§3.5】无 skip：非法/犹豫动作一律视为未决（不输出）
		}
		out2 = append(out2, ReviewDecision{ID: id, Action: action, Note: strings.TrimSpace(d.Note)})
	}
	return out2, nil
}
