package server

// Review service 实现审核 agent 的周期预审、对话以及人工裁决接口。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/xlog"
	"github.com/Autumn-27/norma/llm"
)

// reviewWriteTools 与 guard 共用审核写工具名单，所有写动作都需人工确认。
var reviewWriteTools = intercept.ReviewWriteTools

// seedReviewIntercept 幂等补齐审核写工具的启用项和内置 ask 规则。
// 用户之后仍可禁用或删除规则；确认超时默认 300 秒并自动拒绝。
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
			// 记录启用集补齐结果，便于排查审核写闸门是否生效。
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
			"ask",       // 写动作一律进入人工确认，不调用 LLM 判定。
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
			// 记录种子规则，便于确认审核写动作的保护项是否存在。
			log.Printf("[review] review 写工具拦截规则种子：%s（ask 按钮确认制）", name)
		}
	}
}

// ReviewRunner 周期预审待审意图，并串行化每个任务的审核会话。

type ReviewRunner struct {
	s        *Server
	interval time.Duration
	// wake 合并即时审核请求；ticker 负责周期扫描和漏唤醒恢复。
	wake chan struct{}
	// slots 保证同一任务的预审、自动处置和对话互斥。
	mu        sync.Mutex
	slots     map[string]*reviewSlot
	undecided map[string]int // 自动审核连续全未决轮数（≥3 告警）
}

// newReviewRunner 创建默认每 15 秒扫描一次的审核调度器。
func newReviewRunner(s *Server) *ReviewRunner {
	// ticker 是漏唤醒时的兜底，间隔不宜过长。
	return &ReviewRunner{s: s, interval: 15 * time.Second, wake: make(chan struct{}, 1),
		slots: make(map[string]*reviewSlot), undecided: make(map[string]int)}
}

// reviewSlot 是每任务的进程内审核租约；TTL 允许后续请求接管失效租约。
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

// bumpUndecided 累加自动审核连续全未决轮数并返回当前值。
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
	// 记录扫描周期，便于确认预审调度已启动。
	log.Printf("[review] review 审核轮启动：扫描间隔 %s（预审 + review_auto 自动处置）", rr.interval)
	tick := time.NewTicker(rr.interval)
	defer tick.Stop()
	rr.sweep(ctx) // 启动即扫一轮：重启恢复场景待审意图不该空等一个周期
	for {
		select {
		case <-ctx.Done():
			return
		case <-rr.wake: // 送审或重送后立即唤醒。
			rr.sweep(ctx)
		case <-tick.C:
			rr.sweep(ctx)
		}
	}
}

// wakeReviewRunner 在送审或重送后立即唤醒审核轮；runner 未启动时安全空转。
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

// checkExecutorKey 拒绝未注册、已禁用或内置的建议执行器。
// 内部复用 engine.resolveExecutor（查唯一启用、非内置的自定义 agent 配置）。
func (s *Server) checkExecutorKey(key string) error {
	_, err := s.engine.resolveExecutor(key)
	return err
}

// sweep 遍历有待审意图的任务，只预审内容版本尚未覆盖的意图。
func (rr *ReviewRunner) sweep(ctx context.Context) {
	taskIDs, err := rr.s.m.pg.TasksWithProposed()
	if err != nil {
		log.Printf("[review] sweep 获取待审任务失败: %v", err)
		return
	}
	// 记录本轮发现的待审任务数。
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
// 仅处理 reviewed_version 缺失或落后于 content_version 的意图。
func (rr *ReviewRunner) preReviewTask(ctx context.Context, t *Task) {
	freshNodes, err := t.Store.FreshPendingIntents(50) // 在 SQL 中筛选版本过期的意图。
	if err != nil || len(freshNodes) == 0 {
		return
	}
	// 预审或自动轮未拿到租约时跳过，本轮不排队；下一轮再试。
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
		// provider 不可用时记录告警，避免审核被静默跳过。
		log.Printf("[review] task %s 预审跳过 ⚠ reviewProvider() 为 nil（review agent 缺失/禁用/无 LLM 绑定）——%d 条待审意图无人预审", t.ID, len(fresh))
		return
	}
	// 记录本次审核的意图数和自动处置状态。
	log.Printf("[review] task %s 进入审核轮：%d 条待审意图（review_auto=%v）", t.ID, len(fresh), t.ReviewAuto)
	// ReviewPreScreenConfig.TaskID 是 string 型（agent 侧签名）——t.ID 直接传，不再 ParseInt。
	rr.s.engine.BeginLLMCall(t.ID)
	defer rr.s.engine.EndLLMCall(t.ID)
	cfg := agent.ReviewPreScreenConfig{
		Store: t.Store, Goal: t.Goal, TaskID: t.ID,
		Provider: prov, FreshIDs: fresh,
		Emit: func(a db.Activity) { rr.s.engine.emitActivity(t, a) },
	}
	if t.ReviewAuto { // 开启自动处置时根据预审结论执行后续动作。
		rr.autoDecide(ctx, t, cfg, versions)
		return
	}
	verdicts, err := agent.RunReviewPreScreen(ctx, cfg)
	if err != nil {
		log.Printf("[review] task %s 预审失败: %v", t.ID, err)
		return
	}
	// 预审只写建议并保持 pending；自动处置仅在 review_auto=true 时执行。
	// 写回带版本 CAS，预审期间正文变化的裁决会被丢弃。
	var passed, revised, dropped int
	for id, v := range verdicts {
		note := v.Note
		if len([]rune(note)) > 200 { // 预审摘要最多 200 字。
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

// autoDecide 按模型决策自动批准或驳回，并用版本 CAS 避免覆盖人工并发处置。
// 无法确定的意图保持未决；连续多轮全部未决时记录告警。
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
		// 连续未决可能表示审核提示词或模型异常。
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
			ok, err := t.Store.ApproveIntentAt(d.ID, versions[d.ID]) // 批准时确定执行器。
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
			_, _, ok, err := t.Store.RequestRevisionAt(d.ID, note, versions[d.ID]) // 驳回后退回修订。
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

// reviewProvider 依次尝试任务配置链、review agent 绑定和全局默认 profile。
// 没有可用 provider 时返回 nil，由调用方跳过并记录告警。
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

// 以下 handler 实现审核列表、批准、驳回、对话、消息和自动处置设置。

// reviewListProposed 返回当前任务仍待人工裁决的意图。
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
			// 返回内容版本，供批准和驳回时做 CAS。
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
		// 空值兼容旧客户端；非空时做版本 CAS。
		ExpectedVersion *int `json:"expected_version"`
	}
	if err := decode(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	ver, err := resolveReviewVersion(t.Store, intentID, body.ExpectedVersion)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ok2, err := t.Store.ApproveIntentAt(intentID, ver) // 批准时确定执行器。
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
		// 版本冲突返回 409，前端刷新后再裁决。
		log.Printf("[review] 人工审核 approve 冲突 ⚠ task %s 意图 #%d 已不在待审池（并发重复处置或已驳回）", t.ID, intentID)
		writeErr(w, 409, "意图已被处理（并发冲突）")
		return
	}
	// 记录人工批准的任务、意图和操作者。
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
		ExpectedVersion *int   `json:"expected_version"` // 可空；携带时做版本 CAS。
	}
	if err := decode(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
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
	// 驳回保持 proposed 并标记 rejected，等待修订后重新送审。
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
		// 记录驳回时的版本冲突。
		log.Printf("[review] 人工审核 reject 冲突 ⚠ task %s 意图 #%d 已不在待审池（并发重复处置或已放行）", t.ID, intentID)
		writeErr(w, 409, "意图已被处理（并发冲突）")
		return
	}
	// 记录人工驳回的任务、意图和操作者。
	log.Printf("[review] 人工审核 reject：task %s 意图 #%d 理由=%q（退回修订）", t.ID, intentID, firstLine(reason, 60))
	// §4.2.3 三件套：revision_requested 事件送达 planner（带 IntentID+理由）。
	t.NotifyRevisionRequested(intentID, reason)
	s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "text",
		Summary: fmt.Sprintf("意图 #%d 已驳回：%s", intentID, reason)})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// cancelTaskReview 在任务删除或归档前取消 review 会话，使 inflight drain 能正常结束。
func (s *Server) cancelTaskReview(taskID string) {
	if t, ok := s.m.Task(taskID); ok {
		t.cancelReview()
	}
}

// reviewChat 异步运行审核对话，立即返回 202；写动作仍需 intercept 确认。
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
		// 同任务已有审核会话时记录并返回 409。
		log.Printf("[review] review chat 拒绝 ⚠ task %s 上一轮未结束（busy 互斥）", t.ID)
		writeErr(w, 409, "review 会话正在处理上一条消息")
		return
	}
	// 对话与预审、自动处置共用每任务租约。
	// chat 优先体现在预审侧——预审拿不到槽位直接跳过本轮；chat 拿不到（预审在跑）则 409。
	chatSlot := s.reviewRunnerSlot(t.ID)
	if !chatSlot.acquire("chat", 10*time.Minute) {
		t.reviewBusy.Store(false)
		log.Printf("[review] review chat 拒绝 ⚠ task %s review 槽位被 %s 占用", t.ID, chatSlot.ownerName())
		writeErr(w, 409, "审核轮正在处理该任务，请稍后重试")
		return
	}
	// 把异步会话纳入任务删除/归档屏障，避免数据库删除后仍有 review 写入。
	if !s.engine.beginTaskOperation(t.ID) {
		t.reviewBusy.Store(false)
		chatSlot.release()
		writeErr(w, 409, "任务正在删除或归档，无法发起 review 会话")
		return
	}
	xlog.Debugf("[review] review chat 受理：task %s 消息 %q", t.ID, firstLine(strings.TrimSpace(body.Message), 60))
	msg := strings.TrimSpace(body.Message)
	s.engine.emitActivity(t, db.Activity{Worker: "review", Kind: "user", Summary: msg, Detail: msg})
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Minute)
	t.setReviewCancel(cancel)
	// 删除可能发生在 inflight 注册之后、cancel 注册之前；补查屏障关闭这个窗口。
	if s.engine.IsDeleting(t.ID) {
		cancel()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				xlog.Errorf("[review] chat PANIC task %s: %v\n%s", t.ID, r, debug.Stack())
			}
			cancel()
			t.clearReviewCancel()
			chatSlot.release()
			t.reviewBusy.Store(false)
			s.engine.decInflight(t.ID)
		}()
		// 会话不随 HTTP 请求取消；删除/归档通过 cancelReview 主动终止，3 分钟超时作为最终兜底。
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
			Notify:       t.Notify, // 批准后唤醒 planner/worker。
			NotifyHint:   t.NotifyHint,
			NotifyReview: func(int64) { s.wakeReviewRunner() }, // 送审后立即唤醒审核轮。
			// 驳回后向 planner 发送 revision_requested 事件。
			NotifyRevision: t.NotifyRevisionRequested,
			// 对话内送审或修订同样校验目标执行器。
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
