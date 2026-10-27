package server

// ═══ ARTEX 二开 · [P-REVIEW 审核服务] ═══
// 功能介绍：review agent 的预审 runner（S5）+ 审核五端点 handler（S6）+ intercept ask 化迁移（D9）
// 二开日期：2026-10-21

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/xlog" // 【二开·P-LOG】S5/S6 等点位 DEBUG/INFO 开关
	"github.com/Autumn-27/norma/llm"
)

// ═══ ARTEX 二开 · [P-REVIEW·D9 intercept审核写工具ask化] ═══
// 功能介绍：启动迁移（幂等）——把审核写工具补进 intercept 启用集，并种子对应 [内置] ask 规则（按钮确认制 K15）

// reviewWriteTools 是 review agent 会话轮的写工具集合（K15：全部 ask 化，人点按钮才放行）。
// 【二开·审核流 PR1/§2.8】名单唯一定义移到 intercept.ReviewWriteTools（guard 来源校验共用）。
var reviewWriteTools = intercept.ReviewWriteTools

// seedReviewIntercept ① 把审核写工具补进 intercept 启用集（缺才 append，幂等）；
// ② 种子审核写工具的 [内置] ask 规则（按 name 查重，用户可禁用/删除，与既有内置规则同款待遇）。
// ask 超时默认 300s 自动 deny（intercept/intercept.go:317 defaultJudgeAskTimeoutSecs），
// 前端确认卡需提示时限（U3）。
func (s *Server) seedReviewIntercept() {
	pg := s.m.pg
	if pg == nil || s.m.interceptor == nil {
		return
	}
	// ① 启用集补齐。从未保存过设置时 GetEnabledTools 返回硬编码默认清单
	// （intercept/intercept.go:143：Bash/WebFetch 等 8 个，不含本组）→ 必须主动写回。
	enabled, gErr := s.m.interceptor.GetEnabledTools()
	if gErr != nil {
		log.Printf("[review] intercept 启用集读取失败: %v", gErr)
		return
	}
	seen := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		seen[name] = true
	}
	changed := false
	for _, name := range reviewWriteTools {
		if !seen[name] {
			enabled = append(enabled, name)
			changed = true
		}
	}
	if changed {
		if err := s.m.interceptor.SetEnabledTools(enabled); err != nil {
			log.Printf("[review] intercept 启用集写入失败: %v", err)
		} else {
			// 【二开·P-LOG·D9】INFO 记启用集补齐成功（排障：K15 闸门在不在）
			log.Printf("[review] review 写工具补进 intercept 启用集成功（K15 会话写闸门生效）")
		}
	}
	// ② 三条 [内置] ask 规则种子（按 name 查重幂等）。
	existing, err := pg.ListInterceptRules()
	if err != nil {
		log.Printf("[review] 读取拦截规则失败: %v", err)
		return
	}
	have := make(map[string]bool, len(existing))
	for _, r := range existing {
		have[r.Name] = true
	}
	for _, tool := range reviewWriteTools {
		name := "[内置] 审核写动作确认：" + tool
		if have[name] {
			continue
		}
		_, err := pg.CreateInterceptRule(
			name,        // name
			"tool_name", // match_target：按工具名匹配
			"string",    // match_type：精确串
			tool,        // pattern
			"ask",       // action：一律人工按钮确认（K15），不走 LLM 兜底判官
			"review agent 写动作需人工确认（会话内确认卡放行）", // message
			100,    // priority
			true,   // enabled
			true,   // timeout_enabled
			300,    // timeout_seconds：与 defaultJudgeAskTimeoutSecs 对齐
			"deny", // timeout_action：超时自动拒绝
		)
		if err != nil {
			log.Printf("[review] 种子拦截规则 %s 失败: %v", name, err)
		} else {
			// 【二开·P-LOG·D9】INFO 记种子写入（排障：三条规则在位情况）
			log.Printf("[review] review 写工具拦截规则种子：%s（ask 按钮确认制）", name)
		}
	}
}

// 【二开·P-REVIEW·D9】止

// ═══ ARTEX 二开 · [P-REVIEW·S5 预审 Runner] ═══
// 功能介绍：ReviewRunner——周期扫描有待审意图的任务，review agent 物理只读预审（K14）；
//           fresh 判据=reviewed_version 缺失或落后于 content_version（§3.4，SQL 下推），
//           预审建议 ≤200 字落 payload.review_note（revise 时），人工审核卡片直接展示。
// 二开日期：2026-10-21

type ReviewRunner struct {
	s        *Server
	interval time.Duration
	// 【二开·审核流 PR1/§2.6】wake 是送审/重送成功的即时唤醒通道（容量 1、非阻塞发送，
	// 多个送审合并为一轮 sweep）；ticker 仍是兜底，重启恢复场景由启动即扫覆盖。
	wake chan struct{}
	// 【二开·审核流 PR6/§3.12】每任务 review 槽位（第 1 层进程内租约）：
	// 同一时刻一个任务最多一个 review agent（预审/自动/对话互斥，chat 优先）。
	mu        sync.Mutex
	slots     map[string]*reviewSlot
	undecided map[string]int // 自动审核连续全未决轮数（≥3 告警）
}

func newReviewRunner(s *Server) *ReviewRunner {
	// 【二开·审核流 PR6/§3.11】默认 15s 一轮；送审/重送成功另有 wake 通道即时唤醒（§2.6），
	// ticker 只是重启恢复与漏唤醒场景的兜底，不宜过长。
	return &ReviewRunner{s: s, interval: 15 * time.Second, wake: make(chan struct{}, 1),
		slots: make(map[string]*reviewSlot), undecided: make(map[string]int)}
}

// reviewSlot 每任务 review agent 租约（§3.12 第 1 层：进程内单飞）。
// 租约带 TTL：holder 崩溃不主动释放也会被后续 acquire 抢占（WARN 记抢占）。
type reviewSlot struct {
	mu    sync.Mutex
	busy  bool
	owner string
	until time.Time
}

// acquire 尝试取租约；owner 记占用方（prescreen/auto/chat），ttl 为最长持有期。
func (sl *reviewSlot) acquire(owner string, ttl time.Duration) bool {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	now := time.Now()
	if sl.busy && now.Before(sl.until) {
		return false
	}
	if sl.busy {
		log.Printf("[review] review 槽位被抢占 ⚠ 前 holder=%s 租约过期未释放（疑似崩溃）", sl.owner)
	}
	sl.busy, sl.owner, sl.until = true, owner, now.Add(ttl)
	return true
}

func (sl *reviewSlot) release() {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	sl.busy, sl.owner = false, ""
}

func (sl *reviewSlot) ownerName() string {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return sl.owner
}

func (rr *ReviewRunner) slotFor(taskID string) *reviewSlot {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	sl, ok := rr.slots[taskID]
	if !ok {
		sl = &reviewSlot{}
		rr.slots[taskID] = sl
	}
	return sl
}

// bumpUndecided 累加自动审核连续全未决轮数并返回当前值（§3.5 附注告警判据）。
func (rr *ReviewRunner) bumpUndecided(taskID string) int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.undecided[taskID]++
	return rr.undecided[taskID]
}

func (rr *ReviewRunner) resetUndecided(taskID string) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	delete(rr.undecided, taskID)
}

// Wake 送审/重送成功后立即触发一轮 sweep（非阻塞；扫描中或通道已满时合并，不丢——
// sweep 本身就是全量扫描待审任务）。
func (rr *ReviewRunner) Wake() {
	select {
	case rr.wake <- struct{}{}:
	default:
	}
}

// Run loops until ctx done，每 interval 扫一轮（启动即扫一次）；wake 通道随到随扫。
func (rr *ReviewRunner) Run(ctx context.Context) {
	// 【二开·P-LOG·S5】启动日志：INFO 记启动（排障：预审/自动轮跑没跑、多久扫一次）。
	log.Printf("[review] review 审核轮启动：扫描间隔 %s（预审 + review_auto 自动处置）", rr.interval)
	tick := time.NewTicker(rr.interval)
	defer tick.Stop()
	rr.sweep(ctx) // 启动即扫一轮：重启恢复场景待审意图不该空等一个周期
	for {
		select {
		case <-ctx.Done():
			return
		case <-rr.wake: // 【二开·审核流 PR1/§2.6】送审即唤醒
			rr.sweep(ctx)
		case <-tick.C:
			rr.sweep(ctx)
		}
	}
}

// wakeReviewRunner 送审/重送成功后即时唤醒审核轮（§2.6）。runner 未启动（测试/未装配）时安全空转。
func (s *Server) wakeReviewRunner() {
	if s.reviewRunner != nil {
		s.reviewRunner.Wake()
	}
}

// reviewRunnerSlot 取任务的 review 槽位（runner 未装配时返回一次性槽位，保证 chat 路径不空指针）。
func (s *Server) reviewRunnerSlot(taskID string) *reviewSlot {
	if s.reviewRunner != nil {
		return s.reviewRunner.slotFor(taskID)
	}
	return &reviewSlot{}
}

// checkExecutorKey 校验 target_executor（建议执行器 key，§2.9.3）：未注册/已禁用/内置一律拒绝。
// 内部复用 engine.resolveExecutor（查唯一启用、非内置的自定义 agent 配置）。
func (s *Server) checkExecutorKey(key string) error {
	_, err := s.engine.resolveExecutor(key)
	return err
}

// sweep 遍历有待审意图的任务（D3 TasksWithProposed），逐任务预审 fresh（reviewed_version
// 缺失或落后 content_version，§3.4）的待审意图。
func (rr *ReviewRunner) sweep(ctx context.Context) {
	taskIDs, err := rr.s.m.pg.TasksWithProposed() // D3 第七法
	if err != nil {
		log.Printf("[review] sweep 获取待审任务失败: %v", err)
		return
	}
	// 【二开·P-LOG·S5】拾取日志：INFO 记待审任务数（排障：待审意图为什么没被处理）。
	if len(taskIDs) > 0 {
		log.Printf("[review] 审核轮拾取：%d 个任务有待审意图", len(taskIDs))
	}
	for id := range taskIDs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		t, ok := rr.s.m.Task(fmt.Sprint(id))
		if !ok {
			continue
		}
		rr.preReviewTask(ctx, t)
	}
}

// preReviewTask 对单个任务的 fresh 待审意图跑一轮 review agent 预审。
// BeginLLMCall/EndLLMCall 包裹——预热限流/断网熔断统计与正式 LLM 调用同口径。
// 【二开·审核流 PR6/§3.4】fresh 判据下推 SQL（FreshPendingIntents：reviewed_version 缺失
// 或 < content_version），不再用 review_note 是否为空。
func (rr *ReviewRunner) preReviewTask(ctx context.Context, t *Task) {
	freshNodes, err := t.Store.FreshPendingIntents(50) // §3.4 SQL 下推
	if err != nil || len(freshNodes) == 0 {
		return
	}
	// 【二开·审核流 PR6/§3.12】每任务 review 槽位单飞门（第 1 层进程内租约）：
	// 预审/自动轮拿不到槽位直接跳过本轮（ticker/wake 下轮重试），不排队不并发。
	slot := rr.slotFor(t.ID)
	if !slot.acquire("prescreen", 5*time.Minute) {
		log.Printf("[review] task %s 预审跳过：review 槽位被占用（owner=%s）", t.ID, slot.ownerName())
		return
	}
	defer slot.release()
	fresh := make([]int64, 0, len(freshNodes))
	versions := make(map[int64]int, len(freshNodes))
	for _, n := range freshNodes {
		fresh = append(fresh, n.ID)
		versions[n.ID] = n.ContentVersion
	}
	// review agent 的 LLM：绑定该 agent 的 profile（未绑→全局激活）。
	prov := rr.reviewProvider(t)
	if prov == nil {
		// 【二开·P-LOG·S5】WARN：provider 解析失败导致预审无声跳过（预审轮疑似 bug 的排障眼）
		log.Printf("[review] task %s 预审跳过 ⚠ reviewProvider() 为 nil（review agent 缺失/禁用/无 LLM 绑定）——%d 条待审意图无人预审", t.ID, len(fresh))
		return
	}
	// 【二开·P-LOG·S5】INFO 记进入审核轮（排障：预审轮跑没跑）。
	log.Printf("[review] task %s 进入审核轮：%d 条待审意图（review_auto=%v）", t.ID, len(fresh), t.ReviewAuto)
	// ReviewPreScreenConfig.TaskID 是 string 型（agent 侧签名）——t.ID 直接传，不再 ParseInt。
	rr.s.engine.BeginLLMCall(t.ID)
	defer rr.s.engine.EndLLMCall(t.ID)
	cfg := agent.ReviewPreScreenConfig{
		Store: t.Store, Goal: t.Goal, TaskID: t.ID,
		Provider: prov, FreshIDs: fresh,
		Emit: func(a db.Activity) { rr.s.engine.emitActivity(t, a) },
	}
	if t.ReviewAuto { // 【二开·P-REVIEW·S5b】自动处置分支（任务审核聊天窗 Switch 开启）
		rr.autoDecide(ctx, t, cfg, versions)
		return
	}
	verdicts, err := agent.RunReviewPreScreen(ctx, cfg)
	if err != nil {
		log.Printf("[review] task %s 预审失败: %v", t.ID, err)
		return
	}
	// 【二开·P-REVIEW·人工全审】裁决落账：预审只标注不批准——pass=写 pre_review='pass'+放行理由，
	// revise=写修改建议，两者都保持 pending 等人工处置；自动裁决只在 review_auto=true 的 autoDecide 分支。
	// 全部带版本 CAS：预审期间正文被改的裁决整条丢弃（审的必须是这份稿）。
	var passed, revised, dropped int
	for id, v := range verdicts {
		note := v.Note
		if len([]rune(note)) > 200 { // 红线：预审摘要 ≤200 字（01 册 A8）
			note = string([]rune(note)[:200])
		}
		switch v.Verdict {
		case "pass":
			ok, err := t.Store.SetPrescreenPassNote(id, note, versions[id])
			if err != nil || !ok {
				dropped++
				log.Printf("[review] task %s 意图 #%d 预审 pass 标注落库丢弃（版本冲突/已被处置）: %v", t.ID, id, err)
				continue
			}
			passed++
			rr.s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "text",
				Summary: fmt.Sprintf("意图 #%d 预审建议放行（待人工批准）", id)})
		case "revise":
			ok, err := t.Store.SetPrescreenVerdict(id, note, versions[id])
			if err != nil || !ok {
				dropped++
				log.Printf("[review] task %s 意图 #%d 预审 revise 落库丢弃（版本冲突/已被处置）: %v", t.ID, id, err)
				continue
			}
			revised++
		}
	}
	if passed+revised > 0 {
		// 人工全审：无任何自动放行，不 Notify——意图保持 pending 等人在审核面板处置。
		log.Printf("[review] task %s 预审落账（人工全审）：建议放行 %d / 建议修订 %d / 丢弃 %d", t.ID, passed, revised, dropped)
		rr.s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "text",
			Summary: fmt.Sprintf("预审完成：建议放行 %d 条、建议修订 %d 条（均待人工处置）", passed, revised)})
	}
}

// autoDecide 自动处置一轮（S5b）：LLM 输出决策 JSON，这里经版本 CAS 执行并留痕。
// CAS 失败（人刚好手动处置了同一条）静默跳过——先到先得，与人工面板并发安全。
// 【二开·审核流 PR6/§3.5】决策收窄 approve/reject 两态：拿不准=未决（不输出 id、不做任何
// 写操作）；连续多轮全未决记 WARN 告警（提示 review agent 可能失灵）。
func (rr *ReviewRunner) autoDecide(ctx context.Context, t *Task, cfg agent.ReviewPreScreenConfig, versions map[int64]int) {
	decisions, err := agent.RunReviewAuto(ctx, agent.ReviewAutoConfig{
		Store: cfg.Store, Goal: cfg.Goal, TaskID: cfg.TaskID,
		Provider: cfg.Provider, FreshIDs: cfg.FreshIDs, Emit: cfg.Emit,
	})
	if err != nil {
		log.Printf("[review] task %s 自动审核失败（本轮全部留人）: %v", t.ID, err)
		return
	}
	if len(decisions) == 0 && len(cfg.FreshIDs) > 0 {
		// 连续未决告警（§3.5 附注）：可能 review agent 提示词/模型异常导致不出决策。
		if rr.bumpUndecided(t.ID) >= 3 {
			log.Printf("[review] task %s ⚠ 自动审核连续 3 轮以上全未决（%d 条待审无人处置），请检查 review agent 配置", t.ID, len(cfg.FreshIDs))
		}
	} else if len(decisions) > 0 {
		rr.resetUndecided(t.ID)
	}
	var approved, rejected int
	for _, d := range decisions {
		note := d.Note
		if len([]rune(note)) > 200 {
			note = string([]rune(note)[:200])
		}
		switch d.Action {
		case "approve":
			ok, err := t.Store.ApproveIntentAt(d.ID, versions[d.ID]) // §8：pending_executor 转正
			if err != nil {
				log.Printf("[review] task %s 自动放行 #%d 失败: %v", t.ID, d.ID, err)
				continue
			}
			if !ok {
				continue // CAS 冲突：人抢先处置了
			}
			approved++
			rr.s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "text",
				Summary: fmt.Sprintf("意图 #%d 已自动放行：%s", d.ID, note)})
		case "reject":
			_, _, ok, err := t.Store.RequestRevisionAt(d.ID, note, versions[d.ID]) // §8.1.1：退回修订，不再软删
			if err != nil {
				if errors.Is(err, db.ErrNoteRequired) {
					// 模型驳回了却给不出理由（且节点无既有 review_note）——决策未能执行，
					// 等同未决：计入连续未决告警，避免静默空转（审核员报告 P2-12）。
					if rr.bumpUndecided(t.ID) >= 3 {
						log.Printf("[review] task %s ⚠ 自动驳回连续缺理由（ErrNoteRequired），请检查 review agent 输出", t.ID)
					}
				}
				log.Printf("[review] task %s 自动驳回 #%d 失败: %v", t.ID, d.ID, err)
				continue
			}
			if !ok {
				continue
			}
			rejected++
			// §4.2.3 三件套：revision_requested 事件送达 planner（带 IntentID+理由）。
			t.NotifyRevisionRequested(d.ID, note)
			rr.s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "text",
				Summary: fmt.Sprintf("意图 #%d 已自动驳回（退回修订）：%s", d.ID, note)})
		}
	}
	if approved > 0 {
		t.Notify() // 有放行 → 唤醒 worker 池立即领取
	}
	log.Printf("[review] task %s 自动审核完成：放行 %d / 驳回 %d / 留人 %d", t.ID, approved, rejected, len(cfg.FreshIDs)-approved-rejected)
}

// reviewProvider 解析 review agent 的 LLM provider（绑定 profile 优先，全局激活兜底）。
// reviewProvider 解析 review agent 的 LLM。【二开·P-REVIEW·D2c】m03934「在任务中启动」：
// 优先跟随任务 LLM 配置链（LLMProfileIDs 逐个试）；任务未绑配置时回退 review agent 自身绑定，
// 再回退 providerForProfile(0) 全局默认。全部失败返回 nil（预审跳过并 WARN，S5③）。
func (rr *ReviewRunner) reviewProvider(t *Task) llm.Provider {
	a, err := rr.s.m.pg.GetAgentByKey("review")
	if err != nil || a == nil || !a.Enabled {
		return nil
	}
	for _, pid := range t.LLMProfileIDs {
		if prov, _, ok := rr.s.providerForProfile(pid); ok {
			return prov
		}
	}
	var profileID int64
	if a.LLMProfileID != nil {
		profileID = *a.LLMProfileID
	} else if dp, err := rr.s.m.pg.ActiveProfile(); err == nil && dp != nil {
		// providerForProfile(0) 查 id=0 恒查无此行——「全局默认」必须显式解析
		// is_default 档的 id，否则未绑链的任务预审永远 nil（task 206 实测踩中）。
		profileID = dp.ID
	}
	prov, _, ok := rr.s.providerForProfile(profileID)
	if !ok {
		return nil
	}
	return prov
}

// ═══ ARTEX 二开 · [P-REVIEW·S6 审核五端点] ═══
// 功能介绍：人工审核面板 API——待审列表/批准/驳回/对话/消息页（U3 ReviewPanel 数据源）
// 二开日期：2026-10-21

// reviewListProposed GET /api/tasks/{id}/review/proposed —— 待审意图列表（双条件，m00973/K24）。
func (s *Server) reviewListProposed(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !t.ReviewEnabled {
		// 【二开·P-REVIEW·D2c】任务级总开关（m03934）：未开启审核的任务拒绝审核域请求
		writeErr(w, 409, "该任务未开启意图审核（review_enabled=false）")
		return
	}
	ns, err := t.Store.ProposedIntents(100) // D3
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		out = append(out, map[string]any{
			"id": n.ID, "summary": p["summary"], "priority": n.Priority, "origin": n.Origin,
			"pending_executor": p["pending_executor"], "review_note": p["review_note"],
			"replaces": p["replaces"], "created_at": n.CreatedAt.Unix(),
			// 【二开·审核流 PR2】content_version 给前端做批准/驳回 expected_version CAS。
			"content_version": n.ContentVersion,
		})
	}
	writeJSON(w, 200, map[string]any{"intents": out})
}

// resolveReviewVersion 解析审核端点的 expected_version：显式携带时严格 CAS；
// 缺省（nil，兼容旧前端/旧客户端）=不校验——取节点当前 content_version 代入使 db 侧
// CAS 恒过。注意这是尽力而为的兼容路径：读取到写入之间正文被改的极小窗口仍会被
// db 闸门拒绝（fail-safe），不会产生错误批准。
func resolveReviewVersion(es *db.ExplorationStore, intentID int64, ev *int) (int, error) {
	if ev != nil {
		return *ev, nil
	}
	n, err := es.GetNode(intentID)
	if err != nil {
		return 0, err
	}
	return n.ContentVersion, nil
}

// reviewApprove POST /api/tasks/{id}/review/{intentID}/approve —— 批准（pending_executor 转正，无执行器入参）。
func (s *Server) reviewApprove(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !t.ReviewEnabled {
		// 【二开·P-REVIEW·D2c】任务级总开关（m03934）：未开启审核的任务拒绝审核域请求
		writeErr(w, 409, "该任务未开启意图审核（review_enabled=false）")
		return
	}
	intentID, err := strconv.ParseInt(r.PathValue("intentID"), 10, 64)
	if err != nil || intentID <= 0 {
		writeErr(w, 400, "invalid intent id")
		return
	}
	var body struct {
		// 【二开·审核流 PR3/§8】expected_version 可空：携带时做版本 CAS（前端以 list 返回的
		// content_version 为准），缺省（nil）兼容旧前端=不校验版本（取当前版本代入）。
		ExpectedVersion *int `json:"expected_version"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ver, err := resolveReviewVersion(t.Store, intentID, body.ExpectedVersion)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ok2, err := t.Store.ApproveIntentAt(intentID, ver) // §8：pending_executor 转正
	if err != nil {
		// 【审核-2】db 层以哨兵错误报告 CAS 未命中（状态门禁/版本漂移/节点不存在），
		// HTTP 语义上都是「已被并发处置」→ 409，不是 500。
		if errors.Is(err, db.ErrNotPendingReview) || errors.Is(err, db.ErrStaleVersion) || errors.Is(err, db.ErrIntentNotFound) {
			log.Printf("[review] 人工审核 approve 冲突 ⚠ task %s 意图 #%d：%v", t.ID, intentID, err)
			writeErr(w, 409, "意图已被处理（并发冲突）")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if !ok2 { // CAS 冲突：已不在待审池（并发重复处置）
		// 【二开·P-LOG·S6】WARN 记冲突（排障：明明点了批准为什么没生效）
		log.Printf("[review] 人工审核 approve 冲突 ⚠ task %s 意图 #%d 已不在待审池（并发重复处置或已驳回）", t.ID, intentID)
		writeErr(w, 409, "意图已被处理（并发冲突）")
		return
	}
	// 【二开·P-LOG·S6】INFO 记人工处置（排障：谁批的、批的哪条）。
	log.Printf("[review] 人工审核 approve：task %s 意图 #%d（版本 CAS=%d）", t.ID, intentID, ver)
	t.Notify() // 唤醒 planner/worker 池立即领取放行的 open 意图
	s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "text",
		Summary: fmt.Sprintf("意图 #%d 已批准放行", intentID)})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// reviewReject POST /api/tasks/{id}/review/{intentID}/reject —— 驳回=退回修订（reason 必填；
// 意图保持 proposed 并标记 review_status=rejected，不删除，planner 修订后自动重送审）。
func (s *Server) reviewReject(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !t.ReviewEnabled {
		// 【二开·P-REVIEW·D2c】任务级总开关（m03934）：未开启审核的任务拒绝审核域请求
		writeErr(w, 409, "该任务未开启意图审核（review_enabled=false）")
		return
	}
	intentID, err := strconv.ParseInt(r.PathValue("intentID"), 10, 64)
	if err != nil || intentID <= 0 {
		writeErr(w, 400, "invalid intent id")
		return
	}
	var body struct {
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expected_version"` // 【二开·审核流 PR3/§8】可空，携带时版本 CAS
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		writeErr(w, 400, "驳回理由必填")
		return
	}
	ver, err := resolveReviewVersion(t.Store, intentID, body.ExpectedVersion)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 【二开·审核流 PR5/§8.1.1】驳回=退回修订（保持 proposed + review_status=rejected），不再软删。
	_, _, ok2, err := t.Store.RequestRevisionAt(intentID, reason, ver)
	if err != nil {
		// 【审核-2】同上：CAS 未命中以哨兵错误返回 → 409。
		if errors.Is(err, db.ErrNotPendingReview) || errors.Is(err, db.ErrStaleVersion) || errors.Is(err, db.ErrIntentNotFound) {
			log.Printf("[review] 人工审核 reject 冲突 ⚠ task %s 意图 #%d：%v", t.ID, intentID, err)
			writeErr(w, 409, "意图已被处理（并发冲突）")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if !ok2 { // CAS 冲突：已不在待审池（并发重复处置）
		// 【二开·P-LOG·S6】WARN 记冲突
		log.Printf("[review] 人工审核 reject 冲突 ⚠ task %s 意图 #%d 已不在待审池（并发重复处置或已放行）", t.ID, intentID)
		writeErr(w, 409, "意图已被处理（并发冲突）")
		return
	}
	// 【二开·P-LOG·S6】INFO 记人工处置。
	log.Printf("[review] 人工审核 reject：task %s 意图 #%d 理由=%q（退回修订）", t.ID, intentID, firstLine(reason, 60))
	// §4.2.3 三件套：revision_requested 事件送达 planner（带 IntentID+理由）。
	t.NotifyRevisionRequested(intentID, reason)
	s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "text",
		Summary: fmt.Sprintf("意图 #%d 已驳回：%s", intentID, reason)})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// reviewChat POST /api/tasks/{id}/review/chat —— 与 review agent 对话（异步 202；写动作经 intercept ask 确认卡 K15）。
func (s *Server) reviewChat(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !t.ReviewEnabled {
		// 【二开·P-REVIEW·D2c】任务级总开关（m03934）：未开启审核的任务拒绝审核域请求
		writeErr(w, 409, "该任务未开启意图审核（review_enabled=false）")
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		writeErr(w, 400, "message 必填")
		return
	}
	// 单飞门：同任务同刻只允许一个 review 会话在跑（原子 CAS，与 chatBusy 同款模式）。
	if !t.reviewBusy.CompareAndSwap(false, true) {
		// 【二开·P-LOG·S6】WARN 记忙碌拒绝（排障：消息发了为什么没回）
		log.Printf("[review] review chat 拒绝 ⚠ task %s 上一轮未结束（busy 互斥）", t.ID)
		writeErr(w, 409, "review 会话正在处理上一条消息")
		return
	}
	// 【二开·审核流 PR6/§3.12】chat 也要占 review 槽位（第 1 层租约）：与预审/自动轮互斥。
	// chat 优先体现在预审侧——预审拿不到槽位直接跳过本轮；chat 拿不到（预审在跑）则 409。
	chatSlot := s.reviewRunnerSlot(t.ID)
	if !chatSlot.acquire("chat", 10*time.Minute) {
		t.reviewBusy.Store(false)
		log.Printf("[review] review chat 拒绝 ⚠ task %s review 槽位被 %s 占用", t.ID, chatSlot.ownerName())
		writeErr(w, 409, "审核轮正在处理该任务，请稍后重试")
		return
	}
	// 【二开·P-LOG·S6】DEBUG 记受理。
	xlog.Debugf("[review] review chat 受理：task %s 消息 %q", t.ID, firstLine(strings.TrimSpace(body.Message), 60))
	msg := strings.TrimSpace(body.Message)
	s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "user", Summary: msg, Detail: msg})
	go func() {
		defer t.reviewBusy.Store(false)
		defer chatSlot.release()
		// 会话生命周期跟服务进程（不随 HTTP 请求取消）；3 分钟硬熔断——LLM 通路异常
		// （代理缓冲/对端挂死）时按时失败出错误卡，不再无限死等（历史事故：gzip+SSE
		// 经中转代理被缓冲，连接 ESTAB 死等无任何回执）。
		ctx, cancel := context.WithTimeout(s.ctx, 3*time.Minute)
		defer cancel()
		prov := (&ReviewRunner{s: s}).reviewProvider(t)
		if prov == nil {
			s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "error", Summary: "review agent 不可用（未启用或未绑定 LLM）"})
			return
		}
		s.engine.BeginLLMCall(t.ID)
		defer s.engine.EndLLMCall(t.ID)
		if err := agent.RunReviewChat(ctx, agent.ReviewChatConfig{
			Store: t.Store, Goal: t.Goal, Provider: prov, Message: msg,
			// captureRun 不盖 Worker 章——不补的话回复/thinking/result 行 worker=''，
			// 而 reviewMessages 只查 worker='review' → 回复落库但前端永远看不到。
			Emit: func(a db.Activity) {
				if a.Worker == "" {
					a.Worker = "review"
				}
				s.engine.emitActivity(t, a)
			},
			Notify:       t.Notify, // 【二开·审核流 PR1】批准放行后唤醒 planner/worker 领取
			NotifyHint:   t.NotifyHint,
			NotifyReview: func(int64) { s.wakeReviewRunner() }, // 【二开·审核流 PR1/§2.6】送审即唤醒审核轮
			// 【二开·审核流 PR5/§4.2.3】对话确认驳回 → revision_requested 事件送达 planner。
			NotifyRevision: t.NotifyRevisionRequested,
			// 【二开·审核流 PR8/审核-2 P1】review 对话内送审/修订也要校验 target_executor。
			CheckExecutor: s.checkExecutorKey,
			Hooks:         t.Guard.Hooks(),
		}); err != nil {
			s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "error", Summary: "review 会话异常：" + err.Error()})
		}
	}()
	writeJSON(w, 202, map[string]any{"accepted": true})
}

// reviewMessages GET /api/tasks/{id}/review/messages —— 审核会话消息页（activity 流 worker="review" 过滤）。
func (s *Server) reviewMessages(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !t.ReviewEnabled {
		// 【二开·P-REVIEW·D2c】任务级总开关（m03934）：未开启审核的任务拒绝审核域请求
		writeErr(w, 409, "该任务未开启意图审核（review_enabled=false）")
		return
	}
	acts, _, err := t.Store.ActivityPageWithDetail(db.ActivitySessionFilter{Worker: "review"}, 0, 200) // 审核域消息统一打 worker="review"
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(acts))
	for _, a := range acts {
		kind := a.Kind
		// 聊天面板只呈现对话行：人工/AI 文本(text)、最终答复(result)、错误(error)。
		// thinking/tool_use/tool_result/usage 是执行细节，留在会话转录里，不进聊天流。
		if kind != "text" && kind != "result" && kind != "error" && kind != "user" {
			continue
		}
		// 兼容旧数据：此前人工消息存的是 kind=text+「【人工】」前缀，出口映射成 kind=user 供前端右侧气泡。
		if kind == "text" && strings.HasPrefix(a.Summary, "【人工】") {
			kind = "user"
		}
		out = append(out, map[string]any{
			"id": a.ID, "worker": a.Worker, "kind": kind, "summary": a.Summary, "detail": a.Detail, "is_error": a.IsError, "created_at": a.CreatedAt.Unix(),
		})
	}
	writeJSON(w, 200, map[string]any{"messages": out})
}

// 【二开·P-REVIEW·S5/S6】止
