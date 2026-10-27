package db

// ═══ ARTEX 二开 · 审核流 PR4 ═══
// update_intent / cancel_intent 的存储层（§5/§6/§9）：
//   - UpdateIntent：可修订状态（open / rejected 驳回稿）的原子补丁更新，版本 CAS +
//     产出锁 + 审核重送（rejected→pending）+ 同事务审计 activity，全在一个事务里。
//   - SoftDeleteIntentAt：带 expected_version CAS 的软删除（open / proposed 可取消；
//     running/paused 拒绝并引导 control_work）。不暴露物理删除 CancelIntent。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// update_intent 域哨兵错误：工具层据此给出可操作的报错文案。
var (
	// ErrIntentNotEditable 当前状态不可修改（pending 审核中 / running / paused / 终态）。
	ErrIntentNotEditable = errors.New("intent is not editable in its current state")
	// ErrIntentProduced 产出锁：意图已有 yields/spawns 出边（已产出过事实/发现），
	// 正文不可再改——只能取消后重送新稿。
	ErrIntentProduced = errors.New("intent already produced output; cancel and resubmit instead")
	// ErrNoChange 补丁没有任何实际变化（驳回稿必须改点什么才能重送）。
	ErrNoChange = errors.New("no changes to apply")
)

// IntentPatch 是 update_intent 的可改字段集合（§5.2）。nil=不动该字段。
// 可改：summary / priority / asset_ids / target_executor（写 payload.pending_executor）。
// 不可改：executor（批准时定型）、origin、replaces、state（走审核/执行状态机）。
type IntentPatch struct {
	Summary        *string
	Priority       *int
	AssetIDs       *[]int64
	TargetExecutor *string
}

// IntentUpdate 是一次成功修订的结果回执（工具层原样返回给调用方 + 前端）。
type IntentUpdate struct {
	ID             int64    `json:"intent_id"`
	State          string   `json:"state"`
	ReviewStatus   string   `json:"review_status,omitempty"`
	ContentVersion int      `json:"content_version"`
	Summary        string   `json:"summary"`
	Changed        []string `json:"changed"`
	PreviousNote   string   `json:"previous_note,omitempty"` // 驳回稿原驳回理由（修订参考）
	ReviewReset    bool     `json:"review_reset"`            // true=rejected→pending 已重送审核
}

// UpdateIntent 原子修订一个意图（§6 六步事务）：
//  1. SELECT ... FOR UPDATE 锁行，校验存在性与类型；
//  2. 状态门禁：open 可改；proposed 里仅 rejected（驳回稿）可改——pending（审核中）拒绝；
//  3. expectedVersion 乐观锁（§8.2：正文编辑推 content_version）；
//  4. 产出锁：已有 yields/spawns 出边的意图拒绝正文修改；
//  5. 应用补丁：summary/priority/asset_ids（锚点表整组替换）/target_executor；
//     rejected 稿有任何变化 → review_status 复位 pending、清 review_note、保留
//     revision_count、推版本重送审核；open 稿无变化不推版本，rejected 稿无变化拒绝；
//  6. 同事务写一条审计 activity（worker=auditWorker）。
func (s *ExplorationStore) UpdateIntent(id int64, expectedVersion int, patch IntentPatch, auditWorker string) (*IntentUpdate, error) {
	var out *IntentUpdate
	err := s.withTx(func(tx *sql.Tx) error {
		// ── 1. 锁行读现状 ──
		var rawPayload []byte
		var curVersion, curPriority int
		var state, kind string
		if err := tx.QueryRow(`SELECT payload::text, content_version, priority, state, kind
FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR UPDATE`,
			id, s.expID).Scan(&rawPayload, &curVersion, &curPriority, &state, &kind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrIntentNotFound
			}
			return err
		}
		if kind != "intent" {
			return ErrIntentNotFound
		}
		p := map[string]any{}
		_ = json.Unmarshal(rawPayload, &p)

		// ── 2. 状态门禁 ──
		reviewStatus := strOf(p["review_status"])
		isRejected := state == "proposed" && strOf(p["executor"]) == "review" && reviewStatus == "rejected"
		if state != "open" && !isRejected {
			if state == "proposed" {
				return fmt.Errorf("%w: 意图 #%d 正在审核中（pending），等审核结论或先驳回", ErrIntentNotEditable, id)
			}
			return fmt.Errorf("%w: intent #%d state=%s", ErrIntentNotEditable, id, state)
		}

		// ── 3. 版本 CAS ──
		if expectedVersion != curVersion {
			return fmt.Errorf("%w: expected=%d current=%d（先 list_intents 取最新 content_version）",
				ErrStaleVersion, expectedVersion, curVersion)
		}

		// ── 4. 产出锁 ──
		var produced bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_edges
WHERE exploration_id=$1 AND src_id=$2 AND rel IN ('yields','spawns'))`,
			s.expID, id).Scan(&produced); err != nil {
			return err
		}
		if produced {
			return fmt.Errorf("%w: intent #%d", ErrIntentProduced, id)
		}

		// ── 5. 应用补丁 ──
		changed := make([]string, 0, 4)
		if patch.Summary != nil {
			ns := strings.TrimSpace(*patch.Summary)
			if ns == "" {
				return fmt.Errorf("summary 不能为空")
			}
			if ns != strOf(p["summary"]) {
				p["summary"] = ns
				changed = append(changed, "summary")
			}
		}
		newPriority := curPriority
		if patch.Priority != nil {
			np := *patch.Priority
			if np < 0 {
				np = 0
			}
			if np > 10 {
				np = 10
			}
			if np != curPriority {
				newPriority = np
				changed = append(changed, "priority")
			}
		}
		if patch.TargetExecutor != nil {
			if !isRejected {
				// open 意图不经过审核链，pending_executor 对它永远是死字段（批准转正只发生在
				// proposed 稿）——明确拒绝，避免静默写入产生误导（审核员报告 P2-13）。
				return fmt.Errorf("open 意图无审核链，target_executor 无意义（执行器由送审/批准确定）")
			}
			te := strings.TrimSpace(*patch.TargetExecutor)
			if te != strOf(p["pending_executor"]) {
				if te == "" {
					delete(p, "pending_executor")
				} else {
					p["pending_executor"] = te
				}
				changed = append(changed, "target_executor")
			}
		}
		if patch.AssetIDs != nil {
			cur := int64Slice(p["asset_ids"])
			if !eqInt64s(cur, *patch.AssetIDs) {
				// §10.3-2：空数组=清空——连 payload 键一起删，与「未设置」同态，
				// 下游只需处理「缺失/非空」两种形态（审核员报告 P2-11）。
				if len(*patch.AssetIDs) == 0 {
					delete(p, "asset_ids")
				} else {
					p["asset_ids"] = *patch.AssetIDs
				}
				changed = append(changed, "asset_ids")
			}
		}
		if len(changed) == 0 {
			if isRejected {
				return fmt.Errorf("%w: 驳回稿必须修改内容后才能重送审核", ErrNoChange)
			}
			// open 稿无变化：不推版本、不写审计，直接返回现状（§8.2）。
			out = &IntentUpdate{ID: id, State: state, ReviewStatus: reviewStatus,
				ContentVersion: curVersion, Summary: strOf(p["summary"]), Changed: nil}
			return nil
		}

		previousNote := strOf(p["review_note"])
		reviewReset := false
		if isRejected {
			// 修订完成 → 重送审核：pending + 清驳回理由 + 保留 revision_count（§6）。
			p["review_status"] = "pending"
			delete(p, "review_note")
			reviewReset = true
		}
		newPayload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		newVersion := curVersion + 1
		if _, err := tx.Exec(`UPDATE exploration_nodes
SET payload=$3::jsonb, priority=$4, content_version=$5
WHERE id=$1 AND exploration_id=$2`, id, s.expID, string(newPayload), newPriority, newVersion); err != nil {
			return err
		}
		// 锚点整组替换（payload.asset_ids 与 exploration_anchors 始终同一份）。
		if containsStr(changed, "asset_ids") {
			if _, err := tx.Exec(`DELETE FROM exploration_anchors WHERE node_id=$1`, id); err != nil {
				return err
			}
			for _, aid := range *patch.AssetIDs {
				if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id, asset_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, aid); err != nil {
					return err
				}
			}
		}

		// ── 6. 同事务审计 activity ──
		detail, _ := json.Marshal(map[string]any{
			"changed": changed, "previous_note": previousNote,
			"review_reset": reviewReset, "content_version": newVersion,
		})
		if _, err := tx.Exec(`INSERT INTO activity(exploration_id, node_id, worker, kind, summary, detail)
VALUES ($1,$2,NULLIF($3,''),'text',$4,$5)`,
			s.expID, id, auditWorker,
			fmt.Sprintf("意图 #%d 已修订（%s）%s", id, strings.Join(changed, ","),
				map[bool]string{true: "，已重送审核", false: ""}[reviewReset]),
			string(detail)); err != nil {
			return err
		}

		out = &IntentUpdate{
			ID: id, State: state, ContentVersion: newVersion,
			Summary: strOf(p["summary"]), Changed: changed,
			PreviousNote: previousNote, ReviewReset: reviewReset,
		}
		if reviewReset {
			out.ReviewStatus = "pending"
		} else {
			out.ReviewStatus = reviewStatus
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SoftDeleteIntentAt 带 expected_version CAS 的软删除（§9.1，cancel_intent 用）：
// open / proposed（pending、rejected 均可）→ state='deleted' + delete_reason。
// running/paused 拒绝并引导 control_work（取消执行中意图必须先停 worker）。
// 返回删除前的 summary（供通知文案）。
func (s *ExplorationStore) SoftDeleteIntentAt(id int64, reason string, expectedVersion int) (string, error) {
	var summary string
	err := s.withTx(func(tx *sql.Tx) error {
		var rawPayload []byte
		var curVersion int
		var state string
		if err := tx.QueryRow(`SELECT payload::text, content_version, state FROM exploration_nodes
WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`,
			id, s.expID).Scan(&rawPayload, &curVersion, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrIntentNotFound
			}
			return err
		}
		switch state {
		case "open", "proposed": // 待领/待审/驳回稿都可取消（§9.1）
		case "running", "paused":
			return fmt.Errorf("%w: 意图 #%d 正在执行（state=%s），请先用 control_work 停止执行再取消",
				ErrIntentNotEditable, id, state)
		default:
			return fmt.Errorf("%w: intent #%d state=%s", ErrIntentNotEditable, id, state)
		}
		if expectedVersion != curVersion {
			return fmt.Errorf("%w: expected=%d current=%d（先 list_intents 取最新 content_version）",
				ErrStaleVersion, expectedVersion, curVersion)
		}
		if strings.TrimSpace(reason) == "" {
			reason = "planner 主动取消"
		}
		if _, err := tx.Exec(`UPDATE exploration_nodes
SET state='deleted', delete_reason=$3, blocked_reason=NULL,
    content_version=content_version+1, completed_at=now()
WHERE id=$1 AND exploration_id=$2`, id, s.expID, truncateRunes(reason, 2000)); err != nil {
			return err
		}
		// 副问答会话随删除态原子清理（与 SoftDeleteIntent 同口径）。
		if _, err := tx.Exec(`DELETE FROM side_question_sessions WHERE intent_id=$1`, id); err != nil {
			return err
		}
		var p map[string]any
		if json.Unmarshal(rawPayload, &p) == nil {
			summary = strOf(p["summary"])
		}
		return nil
	})
	return summary, err
}

// int64Slice 把 payload 里的 asset_ids（json 反序列化为 []any）统一成 []int64。
func int64Slice(v any) []int64 {
	arr, _ := v.([]any)
	out := make([]int64, 0, len(arr))
	for _, e := range arr {
		out = append(out, int64(intOf(e)))
	}
	return out
}

func eqInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	// 锚点无序：按多重集比较。
	cnt := make(map[int64]int, len(a))
	for _, x := range a {
		cnt[x]++
	}
	for _, y := range b {
		cnt[y]--
		if cnt[y] < 0 {
			return false
		}
	}
	return true
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
