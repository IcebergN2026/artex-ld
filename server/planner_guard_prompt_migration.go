// planner_guard_prompt_migration.go：planner Guard 提示词一次性迁移（幂等）。
// 主要函数/类型：
//   - transformPlannerGuardPromptBody：纯转换——删除旧「探测理解」段并重编号，
//     非内网行把历史审核提交后缀替换为普通 add_intent 提交（幂等、缺锚保护）。
//   - upgradePlannerGuardPrompts：事务处理内置 planner_templates 六行与当前全局
//     planner prompt，全部成功后写 guard key。
//
// Guard 是实际执行边界（guard/planner_bash_policy.go）；本迁移只负责消除旧文案
// 与 Guard 的冲突，不能替代 Guard。builtin=false 自定义模板、agent_prompts 非当前
// 历史版本、tasks.planner_prompt_override 均不处理。
package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"artex/db"
)

const plannerGuardPromptMigrationKey = "planner_guard_prompt_v1"

// 双锚：旧「探测理解」段的起止（止锚行保留）。历史正文冻结常量——代码侧模板已删除
// 该段，迁移负责把库中存量行同步到同一边界。
const (
	plannerGuardProbeStartAnchor = "3. **（可选，仅开局、极轻量）探测理解**"
	plannerGuardProbeStopAnchor  = "4. **决定补哪些新方向**"
)

// plannerGuardReviewSubmitSuffix 历史审核提交后缀（完整匹配才替换，不模糊覆盖用户文本）。
const plannerGuardReviewSubmitSuffix = `——**若本任务开启了意图审核**（系统提示中有【意图审核已开启】最高优先级约束），改用【一次】add_review_intent 批量送审（参数相同，另可带 target_executor 预选执行器、replaces 标注被替代的旧意图），严禁 add_intent 直投；被驳回的稿在概览 rejected_intents 块（附驳回理由与 revision_count），用 update_intent 修订后自动重送审，确认无价值的用 cancel_intent 取消：`

// transformPlannerGuardPromptBody 对单行正文依次执行：
//  1. 双锚删除旧「探测理解」段（[起锚,止锚)，止锚行保留），并把后续决策编号 4→3、5→4；
//  2. 非内网行把完整匹配的历史审核提交后缀替换为普通提交收尾「：」；
//     内网行保留审核下发内容。
//
// 保护规则：缺锚或锚顺序错误时不切割；两个转换互不影响；已迁移正文再次执行逐字不变。
// 返回 (新正文, 是否有变化, 转换后仍残留旧探测文字)。
func transformPlannerGuardPromptBody(body string, isIntranet bool) (out string, changed bool, probeResidue bool) {
	out = body
	// ① 删除探测段并重编号。
	start := strings.Index(out, plannerGuardProbeStartAnchor)
	if start >= 0 {
		stop := strings.Index(out[start:], plannerGuardProbeStopAnchor)
		if stop > 0 {
			out = out[:start] + out[start+stop:]
			changed = true
		}
		// 锚顺序错误或止锚缺失：不切割，交由 residue 日志人工核对。
	}
	if changed {
		out = strings.Replace(out, "4. **决定补哪些新方向**", "3. **决定补哪些新方向**", 1)
		out = strings.Replace(out, "5. **提交**", "4. **提交**", 1)
	}
	// ② 非内网行的历史审核提交后缀 → 普通提交收尾（完整匹配、幂等）。
	if !isIntranet && strings.Contains(out, plannerGuardReviewSubmitSuffix) {
		out = strings.Replace(out, plannerGuardReviewSubmitSuffix, "：", 1)
		changed = true
	}
	probeResidue = strings.Contains(out, plannerGuardProbeStartAnchor)
	return out, changed, probeResidue
}

// upgradePlannerGuardPrompts 迁移内置 planner_templates 六行与当前全局 planner prompt。
// 单事务处理全部目标行：任一失败回滚且不写 guard key；提交后写 key，写 key 失败
// 下次可重跑（正文转换幂等，不会产生二次变化）。
func upgradePlannerGuardPrompts(pg *db.DB) error {
	_, ok, err := pg.GetSetting(plannerGuardPromptMigrationKey)
	if err != nil || ok {
		return err
	}
	tx, err := pg.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// ① planner_templates 内置六行（内网渗透按 nkey 识别，保留其审核提交内容）。
	rows, err := tx.Query(`SELECT id,nkey,body FROM planner_templates WHERE builtin`)
	if err != nil {
		return err
	}
	type tplRow struct {
		id   int64
		nkey string
		body string
	}
	var tpls []tplRow
	for rows.Next() {
		var r tplRow
		if err := rows.Scan(&r.id, &r.nkey, &r.body); err != nil {
			rows.Close()
			return err
		}
		tpls = append(tpls, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range tpls {
		body, changed, residue := transformPlannerGuardPromptBody(r.body, r.nkey == "内网渗透")
		if residue {
			log.Printf("[prompts] planner Guard 迁移：模板 %s 转换后仍含旧探测文字，请人工核对", r.nkey)
		}
		if !changed {
			continue
		}
		if _, err := tx.Exec(`UPDATE planner_templates SET body=$1,updated_at=now() WHERE id=$2`, body, r.id); err != nil {
			return fmt.Errorf("planner guard prompt migration: planner_templates#%d: %w", r.id, err)
		}
	}

	// ② 当前全局 planner prompt（agents.key='planner' 的 current_prompt_id 指向行；
	// agent_prompts 无 body/updated_at 列，只更新 template_text）。
	var promptID int64
	var promptBody string
	err = tx.QueryRow(`SELECT ap.id, ap.template_text FROM agents a JOIN agent_prompts ap ON ap.id=a.current_prompt_id WHERE a.key='planner'`).Scan(&promptID, &promptBody)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = nil // 尚未 seed 当前 planner prompt：无可迁移行
	case err != nil:
		return fmt.Errorf("planner guard prompt migration: current planner prompt: %w", err)
	}
	if err == nil && promptID != 0 {
		body, changed, residue := transformPlannerGuardPromptBody(promptBody, false)
		if residue {
			log.Printf("[prompts] planner Guard 迁移：当前全局 planner prompt 转换后仍含旧探测文字，请人工核对")
		}
		if changed {
			if _, err := tx.Exec(`UPDATE agent_prompts SET template_text=$1 WHERE id=$2`, body, promptID); err != nil {
				return fmt.Errorf("planner guard prompt migration: agent_prompts#%d: %w", promptID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return pg.SetSetting(plannerGuardPromptMigrationKey, "done")
}
