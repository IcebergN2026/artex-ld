// engine.go：任务执行引擎，驱动 planner/worker 循环、意图领取执行、work 控制与活动落库。
// 主要函数/类型：
//   - Engine：任务执行引擎核心结构（并发映射、exec 上下文、work 控制、steer 队列等）。
//   - NewEngine / Run：创建引擎 / 启动任务的 planner 与 worker 协程。
//   - plannerLoop / workerLoop：规划轮询循环 / worker 领意图执行循环。
//   - runIntent / claimNext：执行单条意图（含 steer 钩子与 per-work 取消）/ 领取下一条意图。
//   - Pause / Resume / StopTask：暂停 / 恢复 / 停止任务的执行。
//   - ControlWork / SteerWork / KillWork：单条 work 的暂停继续、中途纠偏与强制终止。

package server

import (
	"context"
	"encoding/json" // 【二开·P-EXEC·S1】pickExecutor payload.executor 解析
	"errors"
	"fmt"
	"log"
	"runtime/debug" // 【P-LOG·M1】panic 栈
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"artex/agent"
	"artex/db"
	"artex/intercept"
	"artex/xlog" // 【二开·P-LOG】S1/S12 等点位 DEBUG/INFO 开关
	"github.com/jackc/pgx/v5/pgconn"
	"norma/harness"
	"norma/llm"
)

// isFKViolation reports whether err is a Postgres foreign-key violation (SQLSTATE
// 23503) — e.g. an activity insert whose exploration_id has no parent row.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// dropReason classifies why an activity write was dropped, so the log can be
// grouped/analysed by cause rather than by raw error text.
func dropReason(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return "fk_violation(23503,父exploration不存在)"
		case "23505":
			return "unique_violation(23505)"
		default:
			return "pg_error(" + pgErr.Code + ")"
		}
	}
	return "write_error"
}

// bumpDrop increments and returns the running count of dropped (unpersistable)
// activity records for a task. Concurrent planner + worker emits race here, so the
// counter is an atomic behind sync.Map. The count in the log shows loss scale at a
// glance instead of forcing a grep-and-count.
func (e *Engine) bumpDrop(taskID string) int64 {
	v, _ := e.dropCnt.LoadOrStore(taskID, new(int64))
	return atomic.AddInt64(v.(*int64), 1)
}

// preview collapses newlines and trims s to a short rune-safe snippet for one-line
// log output (avoids dumping a multi-KB summary/detail into the log).
func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// model_error（provider/API 故障：LLM 层瞬时重试耗尽，或流已开始后中途断流）
// 收场的 work 不是「试过没做完」也不是真失败，而是外部抖动。默认把它当永久
// blocked 会白丢一条意图，所以这里对该终态额外重跑几次，每次之间退避一下，给
// provider 恢复的时间；重试期间若被暂停/终止/取消则立即让位给对应分支处理。
const (
	modelErrorRetries      = 2               // model_error 收场后额外重试的次数
	modelErrorRetryBackoff = 3 * time.Second // 每次重试前的退避
	workControlWaitTimeout = 30 * time.Second
)

var errWorkControlConflict = errors.New("work control conflict")

// retryableWorkerModelError excludes errors already handled by the task router.
// In particular, a quota error after partial streaming advances the task cursor
// for the next LLM call but must not replay this whole intent on the backup.
func retryableWorkerModelError(reason harness.TerminalReason, err error) bool {
	return reason == harness.ReasonModelError && !isTaskLLMRuntimeError(err)
}

// Engine drives the event-driven exploration loop with real LLM agents
// (docs §4.3/§4.4): on asset/exploration-graph change (debounced) it wakes the
// planner, which reads the route, queries assets, judges goals and emits intents;
// N concurrent work agents claim intents and execute them. There is no
// simulation mode — an LLM provider is required. The planner/worker can be
// (re)installed at runtime (LLM configured from the UI); the loops always run
// but idle until an LLM is set.
type Engine struct {
	m        *Manager
	debounce time.Duration

	bc *Broadcaster // live activity pub/sub (SSE)

	// 【二开·P-EXEC·S1】providerFn：执行器 agent 的 LLM 装配回调——server 层 providerForProfile 注入（engine 不反向依赖 Server）
	providerFn func(profileID int64) (llm.Provider, agent.Config, bool)
	// preAgentFn 在现有 Conversation/ChatAgent 链中执行首个种子意图。
	// 返回 claimed=false 表示对话未领取，Engine 保持种子原状且不启动普通 Worker。
	preAgentFn func(ctx context.Context, t *Task) (claimed bool, err error)

	started  sync.Map // taskID -> bool, so Run is idempotent per task
	lastAct  sync.Map // taskID -> int64 unix, last planner/worker activity (heartbeat)
	llmCalls sync.Map // taskID -> *int64, actual planner/worker/main-agent LLM calls
	paused   sync.Map // taskID -> bool, user-paused (planner + workers idle but loops alive)
	deleting sync.Map // taskID -> bool, delete barrier (no new task-owned writes)
	dropCnt  sync.Map // taskID -> *int64, running count of dropped (unpersistable) activity records

	// deleteMu makes installing the delete barrier atomic with registering a new
	// task operation. Once BeginDelete returns, every admitted writer is reflected
	// in inflight and every later writer is rejected.
	deleteMu sync.RWMutex

	// Every long-lived task goroutine (planner, workers and deadline coordinator)
	// runs under one task-scoped context. Successful deletion cancels that context,
	// waits for all goroutines, then releases every task-level Engine reference.
	runtimeMu sync.Mutex
	runtimes  map[string]*taskRuntime

	// per-task execution context: each planner.Plan / worker.Execute runs under it,
	// so pausing can CANCEL an in-flight run (not just skip the next one). Recreated
	// on resume since cancelling is one-shot. Every cancellation carries a named
	// cause so the activity trace can identify the initiating control path.
	execMu     sync.Mutex
	execCancel map[string]context.CancelCauseFunc
	execCtx    map[string]context.Context

	// Per-work control lets the planner kill a worker and lets the UI pause/cancel
	// one intent without pausing the whole task. The done channel closes only after
	// runWorkerStep has stopped writing and committed its final state.
	workMu sync.Mutex
	work   map[int64]*workExecution

	// steerBox queues planner course-corrections for a running work (keyed by intent
	// id). The worker's PreToolUse hook drains it before its next tool call and hands
	// the message to the model (blocking that call) so it re-plans — no kill needed.
	steerMu  sync.Mutex
	steerBox map[int64][]string

	plannerRound sync.Map // taskID -> int, planner round counter (for UI round separators)

	// 任务级超时(见 docs/任务级超时与收尾设计.md):
	settling     sync.Map // taskID -> bool, 任务已进入收尾时序(停止派/领新意图)
	deadline     sync.Map // taskID -> int64 unix, 绝对截止时刻(首次运行时盖章;0/缺省=不限)
	stamped      sync.Map // taskID -> bool, first_run_at 是否已盖章(本进程内只盖一次)
	inflight     sync.Map // taskID -> *int64, 在跑的 planner.Plan + worker.Execute 计数(用于 drain)
	coordStarted sync.Map // taskID -> bool, deadline 协调器是否已启动(Run/reload 去重)

	// resolve returns a task's dedicated planner/worker (wired by the server as the
	// authoritative task-router). nil,nil means this task is deliberately unavailable
	// (for example an exhausted failover chain) — there is no global-pair fallback.
	resolve              func(t *Task) (*agent.Planner, *agent.Worker)
	resolveAuthoritative bool
	// readiness reports whether a global LLM provider is configured — the signal behind
	// Ready()/the llm_configured indicator. Wired once at startup; nil → not ready.
	readiness func() bool
}

type taskRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type workExecution struct {
	cancel context.CancelCauseFunc
	done   chan error
	action string // user action: pause | cancel
}

// nextPlannerRound returns the next planner round number for a task (1-based).
func (e *Engine) nextPlannerRound(taskID string) int {
	v, _ := e.plannerRound.LoadOrStore(taskID, 0)
	n := v.(int) + 1
	e.plannerRound.Store(taskID, n)
	return n
}

// Pause stops a task: marks it paused AND cancels any in-flight planner/worker run
// for it (a long worker.Execute would otherwise keep going until it finishes).
func (e *Engine) Pause(taskID string, cause error) {
	e.paused.Store(taskID, true)
	e.cancelExec(taskID, cause)
}

// BeginDelete installs an execution barrier before task data/files are removed.
// The temporary pause is not a user pause. The server serializes this transition
// with lifecycle admission and tells AbortDelete whether the persisted task is
// paused/queued if cleanup fails.
func (e *Engine) BeginDelete(taskID string) bool {
	e.deleteMu.Lock()
	if _, loaded := e.deleting.LoadOrStore(taskID, true); loaded {
		e.deleteMu.Unlock()
		return false
	}
	e.paused.Store(taskID, true)
	e.deleteMu.Unlock()
	e.cancelExec(taskID, agent.AbortTaskDeleted)
	return true
}

func (e *Engine) AbortDelete(taskID string, keepPaused bool) {
	e.deleteMu.Lock()
	if !e.IsDeleting(taskID) {
		e.deleteMu.Unlock()
		return
	}
	e.deleting.Delete(taskID)
	if !keepPaused {
		e.paused.Delete(taskID)
	}
	e.deleteMu.Unlock()
	if !keepPaused && e.m != nil {
		if t, ok := e.m.Task(taskID); ok {
			t.Notify()
		}
	}
}

func (e *Engine) IsDeleting(taskID string) bool {
	_, ok := e.deleting.Load(taskID)
	return ok
}

// registerTaskRoutines reserves count goroutines in the task runtime. Callers
// hold deleteMu for reading so StopTask cannot race WaitGroup.Add with Wait.
func (e *Engine) registerTaskRoutines(parent context.Context, taskID string, count int) *taskRuntime {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	rt := e.runtimes[taskID]
	if rt == nil {
		ctx, cancel := context.WithCancel(parent)
		rt = &taskRuntime{ctx: ctx, cancel: cancel}
		e.runtimes[taskID] = rt
	}
	rt.wg.Add(count)
	return rt
}

// runTaskRoutine 启动任务 goroutine。【P-LOG·M1】recover 必须在本函数内：此前无 recover，
// planner/worker goroutine panic 直接杀进程，且 Go runtime 的栈直写 stderr 绕过 logsink，
// /api/logs 与 server_logs 永远看不到。recover 后任务由既有「worker 不活跃检测」标 failed。
func runTaskRoutine(rt *taskRuntime, name string, fn func(context.Context)) {
	go func() {
		defer rt.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				xlog.Errorf("[engine] goroutine PANIC (%s): %v\n%s", name, r, debug.Stack())
			}
		}()
		fn(rt.ctx)
	}()
}

// StopTask permanently stops every long-lived goroutine and removes all Engine
// state for a successfully deleted task. The delete barrier remains installed
// until cleanup finishes, so no new task operation can race the teardown.
func (e *Engine) StopTask(taskID string) {
	e.deleteMu.Lock()
	e.deleting.Store(taskID, true)
	e.deleteMu.Unlock()

	e.cancelExec(taskID, agent.AbortTaskDeleted)
	e.runtimeMu.Lock()
	rt := e.runtimes[taskID]
	if rt != nil {
		rt.cancel()
	}
	e.runtimeMu.Unlock()
	if rt != nil {
		rt.wg.Wait()
	}

	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(agent.AbortTaskDeleted)
	}
	delete(e.execCancel, taskID)
	delete(e.execCtx, taskID)
	e.execMu.Unlock()

	e.runtimeMu.Lock()
	if e.runtimes[taskID] == rt {
		delete(e.runtimes, taskID)
	}
	e.runtimeMu.Unlock()

	e.started.Delete(taskID)
	e.lastAct.Delete(taskID)
	e.llmCalls.Delete(taskID)
	e.paused.Delete(taskID)
	e.dropCnt.Delete(taskID)
	e.plannerRound.Delete(taskID)
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.inflight.Delete(taskID)
	e.coordStarted.Delete(taskID)
	e.deleteMu.Lock()
	e.deleting.Delete(taskID)
	e.deleteMu.Unlock()
}

// cancelExec cancels a task's current per-task exec context (any in-flight
// planner.Plan / worker.Execute), if present. Shared by Pause and the settle
// sequence's hard-drain backstop.
func (e *Engine) cancelExec(taskID string, cause error) {
	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(cause)
	}
	e.execMu.Unlock()
}

// Resume un-pauses a task and nudges a fresh planning round. The next exec under
// it gets a fresh (uncancelled) context.
func (e *Engine) Resume(t *Task) {
	// BeginDelete owns the pause barrier once deletion starts. A concurrent
	// resume must never clear it and let a planner/worker re-enter while cleanup
	// is waiting for task operations to drain.
	if t == nil {
		return
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(t.ID) {
		return
	}
	e.paused.Delete(t.ID)
	t.Notify()
}

// execContextFor returns a live per-task context derived from parent, recreating
// it if a prior pause cancelled it.
func (e *Engine) execContextFor(parent context.Context, taskID string) context.Context {
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if e.IsPaused(taskID) {
		// never hand out a live context while paused (guards the claim→Execute race)
		c, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortPausedRaceGuard)
		return c
	}
	if c := e.execCtx[taskID]; c != nil && c.Err() == nil {
		return c
	}
	c, cancel := context.WithCancelCause(parent)
	e.execCtx[taskID] = c
	e.execCancel[taskID] = cancel
	return c
}

// IsPaused reports whether a task is user-paused.
func (e *Engine) IsPaused(taskID string) bool {
	v, ok := e.paused.Load(taskID)
	return ok && v.(bool)
}

// Started reports whether the engine loops are running for a task.
func (e *Engine) Started(taskID string) bool {
	_, ok := e.started.Load(taskID)
	return ok
}

// LastActivity returns the unix time of the last planner/worker activity for a
// task (0 if none yet).
func (e *Engine) LastActivity(taskID string) int64 {
	if v, ok := e.lastAct.Load(taskID); ok {
		return v.(int64)
	}
	return 0
}

// BeginLLMCall/EndLLMCall track actual provider calls separately from the
// scheduler's task-operation counter. A task can have live loops while all of
// them are waiting for a trigger; that state must remain idle in the UI.
func (e *Engine) BeginLLMCall(taskID string) {
	v, _ := e.llmCalls.LoadOrStore(taskID, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (e *Engine) EndLLMCall(taskID string) {
	if v, ok := e.llmCalls.Load(taskID); ok {
		p := v.(*int64)
		if atomic.AddInt64(p, -1) <= 0 {
			atomic.StoreInt64(p, 0)
		}
	}
}

func (e *Engine) ActiveLLMCalls(taskID string) int64 {
	if v, ok := e.llmCalls.Load(taskID); ok {
		return atomic.LoadInt64(v.(*int64))
	}
	return 0
}

func (e *Engine) touch(taskID string) { e.lastAct.Store(taskID, time.Now().Unix()) }

func NewEngine(m *Manager) *Engine {
	return &Engine{m: m, debounce: 800 * time.Millisecond, bc: NewBroadcaster(),
		execCancel: map[string]context.CancelCauseFunc{}, execCtx: map[string]context.Context{},
		work: map[int64]*workExecution{}, steerBox: map[int64][]string{},
		runtimes: map[string]*taskRuntime{}}
}

// registerWork records the cancel for the work currently running intentID.
func (e *Engine) registerWork(intentID int64, cancel context.CancelCauseFunc) {
	e.workMu.Lock()
	e.work[intentID] = &workExecution{cancel: cancel, done: make(chan error, 1)}
	e.workMu.Unlock()
}

// detachWork removes the live control handle once Execute has returned. complete
// must be called after the final intent state write so a waiting cancel handler can
// safely delete the worker's blackboard output without racing a late write.
func (e *Engine) detachWork(intentID int64) (action string, complete func(error)) {
	e.workMu.Lock()
	run := e.work[intentID]
	if run != nil {
		delete(e.work, intentID)
		action = run.action
		run.cancel(agent.AbortWorkFinished) // release resources (no-op if already cancelled)
	}
	e.workMu.Unlock()
	e.steerMu.Lock()
	delete(e.steerBox, intentID) // drop any undelivered steering for a finished work
	e.steerMu.Unlock()
	if run == nil {
		return action, func(error) {}
	}
	return action, func(err error) { run.done <- err }
}

// ControlWork requests a user-visible pause or cancellation and waits until the
// worker has fully stopped writing. Cancellation cleanup is performed by the API
// handler after this returns; pause state is committed by runWorkerStep itself.
func (e *Engine) ControlWork(ctx context.Context, intentID int64, action string) error {
	if action != "pause" && action != "cancel" {
		return fmt.Errorf("unsupported work action %q", action)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.workMu.Lock()
	run := e.work[intentID]
	if run == nil {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 意图 %d 当前没有运行中的 work（可能已结束或未被领取）", errWorkControlConflict, intentID)
	}
	if run.action != "" {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 意图 %d 正在执行 %s 操作", errWorkControlConflict, intentID, run.action)
	}
	run.action = action
	done := run.done
	cause := error(agent.AbortWorkPausedByUser)
	if action == "cancel" {
		cause = agent.AbortWorkCancelledByUser
	}
	run.cancel(cause)
	e.workMu.Unlock()

	timer := time.NewTimer(workControlWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("等待意图 %d %s 收尾: %w", intentID, action, ctx.Err())
	case <-timer.C:
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("等待意图 %d %s 收尾: %w", intentID, action, context.DeadlineExceeded)
	}
}

// releaseWorkControl drops only this caller's reservation after its wait is
// cancelled. The work context stays cancelled; runWorkerStep recognizes the
// named cancellation cause and settles the intent into the recoverable paused
// state even if the HTTP caller has gone away.
func (e *Engine) releaseWorkControl(intentID int64, run *workExecution, action string) {
	e.workMu.Lock()
	if current := e.work[intentID]; current == run && current.action == action {
		current.action = ""
	}
	e.workMu.Unlock()
}

func transitionIntentState(store *db.ExplorationStore, intentID int64, state string) error {
	current, err := store.GetNode(intentID)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("%w: 意图 %d 不存在或已删除", db.ErrIntentStateConflict, intentID)
	}
	changed, err := store.CompareAndSetIntentState(intentID, current.State, state)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: 意图 %d 不再是 %s 状态", db.ErrIntentStateConflict, intentID, current.State)
	}
	return nil
}

// SteerWork queues a mid-run course-correction for the work running intentID (the
// planner's steer_work tool). The worker delivers it before its next tool call and
// re-plans — no kill. Errors if no work is currently running that intent.
func (e *Engine) SteerWork(intentID int64, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("纠偏消息不能为空")
	}
	e.workMu.Lock()
	running := e.work[intentID] != nil
	e.workMu.Unlock()
	if !running {
		return fmt.Errorf("意图 %d 当前没有运行中的 work（可能已结束或未被领取）", intentID)
	}
	e.steerMu.Lock()
	e.steerBox[intentID] = append(e.steerBox[intentID], msg)
	e.steerMu.Unlock()
	return nil
}

// drainSteer pops the oldest queued steering message for intentID (FIFO), if any.
func (e *Engine) drainSteer(intentID int64) (string, bool) {
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	q := e.steerBox[intentID]
	if len(q) == 0 {
		return "", false
	}
	msg := q[0]
	if len(q) == 1 {
		delete(e.steerBox, intentID)
	} else {
		e.steerBox[intentID] = q[1:]
	}
	return msg, true
}

// steerHooks wraps the guard's hook runner so the planner can steer a running work:
// before each tool call it drains a queued course-correction (if any) and blocks the
// call, handing the message back to the model — which re-plans its next step instead
// of running the tool. No queued message → the guard behaves exactly as before.
// 它同时负责「空转回合」的续跑，见 Stop。
type steerHooks struct {
	inner harness.HookRunner
	drain func() (string, bool)
	// nudges 是本条意图已注入的空转续跑次数，上限 limit。指针:harness 持有的是
	// steerHooks 的值拷贝，计数必须共享同一份。
	nudges *atomic.Int64
	// limit 是空转续跑的次数上限，由 Engine.emptyTurnNudgeLimit() 从「空响应重试
	// 次数」解析而来。<=0 = 不介入(用户显式关掉了这层)。
	limit int
	// label 形如 "worker-1 · #42"，只用于日志。
	label string
}

// 空转回合(只有思考、既无正文也无工具调用)续跑次数的默认值，与 SDK 空响应重试的
// 内置默认(norma/llm/openai.go 的 emptyResponseRetries)保持一致——两层共用同一个
// 旋钮，不配置时的行为也该对齐。解析见 Engine.emptyTurnNudgeLimit。
//
// 注意这个数是「一条意图的总量」，不是「连续几次」:harness 自身的 stopHookActive
// 已经限死了连续空转只推一次——推完那一轮若还是空转，Stop 钩子不会再被调到，run 直接
// 收场;只有真正发生过一次工具回合，配额才刷新(third_party/norma/harness/query.go#loop.run)。所以这个
// 闸挡的是「工具 → 空转 → 推 → 工具 → 空转」这种病态循环，别让它把意图预算耗光。
const defaultEmptyTurnNudges = 2

// emptyTurnNudge 是空转回合注入的续跑指令。
//
// 这种回合在 harness 眼里是一次自然结束(stop_reason=end_turn 且无 tool_use)，五层
// LLM 重试一层都不适用——它不是错误，是模型「想完了但没动手」。SDK 的空响应重试也
// 够不着:它以「有没有 yield 过事件」判空，而思考增量本身就是事件(norma/llm/openai.go
// 的 SEThinkingDelta)，所以 thinking-only 不算空。何况那层是原样重发整个 prompt，
// 对这种由上下文形状决定的空转，重发只会让模型再想一遍。这里换成追加一条指令，让它
// 带着已经产出的思考继续，输入变了才有理由给出不同的行为。
const emptyTurnNudge = "【空转提醒】你上一轮只输出了思考过程，既没有给出正文回复，也没有调用任何工具，" +
	"这一轮等于没有产出。请直接执行你刚才想好的下一步：要么调用工具，要么给出结论文字。不要重复思考。"

// isThinkingOnlyTurn reports whether the latest assistant turn produced neither
// text nor a tool call — i.e. the model spent the whole round thinking.
func isThinkingOnlyTurn(messages []llm.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != llm.RoleAssistant {
			continue
		}
		return strings.TrimSpace(m.Text()) == "" && len(m.ToolUses()) == 0
	}
	return false
}

func (h steerHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if msg, ok := h.drain(); ok {
		return true, "【规划者实时纠偏】" + msg +
			"\n（这是规划者对本意图的即时指令；本次工具调用未执行，请据此调整下一步。若与你当前打算冲突，以此为准。）", nil
	}
	if h.inner != nil {
		return h.inner.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}

func (h steerHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if h.inner != nil {
		h.inner.PostToolUse(ctx, name, input, result, isErr)
	}
}

// Stop 在 guard 原有语义之上补一层「空转回合」续跑:模型只输出了思考、既没给正文
// 也没调工具时，harness 会把它当成自然结束并以空 summary 收场(query.go 的
// ReasonCompleted + asst.Text())，一条本来还没做完的意图就这样断在半路。此时注入
// 一条续跑指令，让模型带着已有思考接着走。
func (h steerHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	var (
		prevent  bool
		blocking []string
		msg      string
	)
	if h.inner != nil {
		prevent, blocking, msg = h.inner.Stop(ctx, messages)
	}
	// inner 已经决定硬停、或已经要注入自己的续跑消息 → 尊重它，不再叠加。
	// limit<=0 = 用户把「空响应重试次数」配成了 -1，即显式关掉这层。
	if prevent || len(blocking) > 0 || h.nudges == nil || h.limit <= 0 || !isThinkingOnlyTurn(messages) {
		return prevent, blocking, msg
	}
	n := h.nudges.Add(1)
	if n > int64(h.limit) {
		log.Printf("[work %s] 空转回合(仅思考、无正文无工具)已达续跑上限 %d，放行收场", h.label, h.limit)
		return prevent, blocking, msg
	}
	log.Printf("[work %s] 空转回合(仅思考、无正文无工具)，注入续跑指令 (%d/%d)", h.label, n, h.limit)
	return false, []string{emptyTurnNudge}, ""
}

// KillWork cancels the in-flight work running intentID (planner's kill_work tool).
// The work's agent-core session honors ctx cancellation and aborts promptly.
func (e *Engine) KillWork(intentID int64) error {
	e.workMu.Lock()
	run := e.work[intentID]
	e.workMu.Unlock()
	if run == nil {
		return fmt.Errorf("意图 %d 当前没有运行中的 work（可能已结束或未被领取）", intentID)
	}
	run.cancel(agent.AbortKilledByPlanner)
	return nil
}

// Broadcaster exposes the engine's live activity pub/sub (used by the SSE handler).
func (e *Engine) Broadcaster() *Broadcaster { return e.bc }

// emitActivity persists one captured step AND fans it out to live subscribers,
// from a single point so storage and the SSE stream never diverge.
func (e *Engine) emitActivity(t *Task, r db.Activity) db.Activity {
	id, err := e.appendActivity(t, r)
	if err != nil {
		// NO LONGER SILENT: dropping a record breaks command↔result pairing in the
		// trace — a tool_use whose tool_result was lost shows as "执行中" forever, and
		// a lost 'result'/'round' record leaves the session with no summary ("无总结").
		// Everything needed to分析根因 goes into ONE error-level line: reason class,
		// summary preview, running drop count for this task, and — on the FK case — a
		// live probe of WHY the parent exploration is unreachable.
		n := e.bumpDrop(t.ID)
		diag := ""
		// On the FK-parent failure (23503) probe the live DB so the log records WHY the
		// exploration is unreachable (row gone / wrong expID) instead of just that it is.
		if isFKViolation(err) {
			storeID := t.Store.ID()
			if exists, refs, maxID, dErr := e.m.pg.ExplorationDiag(storeID); dErr != nil {
				diag = fmt.Sprintf(" | FK诊断查询失败(store.expID=%d task.ExpID=%d): %v", storeID, t.ExpID, dErr)
			} else {
				diag = fmt.Sprintf(" | FK诊断: store.expID=%d task.ExpID=%d exploration存在=%v 引用它的task数=%d MAX(exploration.id)=%d",
					storeID, t.ExpID, exists, refs, maxID)
			}
		}
		log.Printf("[activity] task %s 丢弃活动记录(该任务累计第 %d 条) worker=%s kind=%s tool=%s tuid=%s reason=%s summary=%q: %v%s",
			t.ID, n, r.Worker, r.Kind, r.Tool, r.ToolUseID, dropReason(err), preview(r.Summary, 80), err, diag)
		e.touch(t.ID)
		return r
	}
	r.ID = id
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	e.bc.Publish(t.ID, r)
	e.touch(t.ID)
	return r
}

// appendActivity persists one activity row, retrying briefly on write failure.
// Concurrent planner + worker inserts into the same exploration's activity log
// occasionally fail; a couple of quick retries recover most. Crucially, every
// failure is now LOGGED (it used to be swallowed by an `if err == nil`), so the
// underlying DB error is finally visible for diagnosis.
func (e *Engine) appendActivity(t *Task, r db.Activity) (int64, error) {
	var id int64
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if id, err = t.Store.AppendActivity(r); err == nil {
			if attempt > 1 {
				log.Printf("[activity] task %s 写入第 %d 次重试成功 (worker=%s kind=%s tool=%s)",
					t.ID, attempt, r.Worker, r.Kind, r.Tool)
			}
			return id, nil
		}
		log.Printf("[activity] task %s 写入失败 (第 %d/3 次, worker=%s kind=%s tool=%s expID=%d): %v",
			t.ID, attempt, r.Worker, r.Kind, r.Tool, t.Store.ID(), err)
		time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
	}
	return 0, err
}

// SetReadiness wires the global "an LLM provider is configured" predicate (read by
// Ready() / the llm_configured indicator). Called once at startup.
func (e *Engine) SetReadiness(fn func() bool) { e.readiness = fn }

// SetAgentResolver installs a per-task planner/worker resolver (wired by the server).
// Called once at startup before any task loop runs, so no lock is needed on reads.
func (e *Engine) SetAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = false
}

// SetAuthoritativeAgentResolver installs a resolver whose nil result must not
// fall through to the global provider. Task-level failover chains use this so a
// fully exhausted chain cannot silently bypass its configured boundary.
func (e *Engine) SetAuthoritativeAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = true
}

// snapshotFor returns the planner/worker a task should run on, from the task-router
// resolver. nil,nil means the task is deliberately unavailable (e.g. an exhausted
// failover chain); there is no global-pair fallback.
func (e *Engine) snapshotFor(t *Task) (*agent.Planner, *agent.Worker) {
	if e.resolve != nil {
		p, w := e.resolve(t)
		if (p != nil && w != nil) || e.resolveAuthoritative {
			return p, w
		}
	}
	return nil, nil
}

// Ready reports whether a global LLM provider is configured (via the readiness
// predicate wired at startup).
func (e *Engine) Ready() bool {
	return e.readiness != nil && e.readiness()
}

// ReadyFor reports whether a specific task can resolve a planner/worker pair.
// An explicit task profile chain can be runnable even when no global default
// provider is configured, so task status must not rely on Ready alone.
func (e *Engine) ReadyFor(t *Task) bool {
	p, w := e.snapshotFor(t)
	return p != nil && w != nil
}

// Run starts the planner loop + N worker loops for a task. The loops always run
// but no-op until an LLM is configured (so a task created while idle picks up
// automatically once LLM is set from the UI).
func (e *Engine) Run(ctx context.Context, t *Task) {
	workers := e.m.Workers()
	// 【二开·P-TPL·S12】起 —— 任务级 worker 数覆盖：tasks.worker_count>0 时按任务起池
	// （0=跟随全局默认，见 00 册 D2/D12 语义）。合法性在建任务时已拦（S13：0~16）。
	// 仅启动时读一次，运行中不可改；重启恢复走同一 Run，列值再次生效。
	if t.WorkerCount > 0 {
		workers = t.WorkerCount
		// 【二开·P-LOG·S12】INFO 记覆盖生效值（排障：这个任务起了几个 worker）
		log.Printf("[engine] task %s worker 池任务级覆盖生效：worker_count=%d（全局默认 %d）", t.ID, t.WorkerCount, e.m.Workers())
	} else {
		xlog.Debugf("[engine] task %s worker 池跟随全局默认：%d（worker_count=0）", t.ID, workers)
	}
	// 【二开·P-TPL·S12】止
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.started.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		t.Notify() // already running — just nudge a planning round
		return
	}
	routineCount := 1 + workers
	if t.PreAgent != "" {
		routineCount = 2 + workers // planner + custom wrapper + custom 结束后启动的 Worker 池
	}
	rt := e.registerTaskRoutines(ctx, t.ID, routineCount)
	e.deleteMu.RUnlock()
	e.touch(t.ID)
	runTaskRoutine(rt, "planner task="+t.ID, func(loopCtx context.Context) { e.plannerLoop(loopCtx, t) })
	if t.PreAgent != "" {
		// 首个种子只由 Conversation/ChatAgent 执行；完成前不启动普通 Worker 池。
		runTaskRoutine(rt, "executor "+t.PreAgent+" task="+t.ID, func(loopCtx context.Context) {
			workersStarted := false
			defer func() {
				if !workersStarted {
					// registerTaskRoutines 已预留延迟 Worker；wrapper 的 Done 尚未执行，释放不会变负。
					rt.wg.Add(-workers)
				}
			}()
			if !e.runPreAgentStage(loopCtx, t) {
				return
			}
			if loopCtx.Err() != nil || e.IsDeleting(t.ID) || e.isSettling(t.ID) || isTerminalStatus(e.m.TaskStatus(t.ID)) {
				return
			}
			workersStarted = true
			e.startWorkerLoops(loopCtx, rt, t, workers)
		})
	} else {
		e.startWorkerLoops(ctx, rt, t, workers)
	}
	e.startDeadlineCoordinator(ctx, t) // 任务级超时定时器(仅 timeout>0;去重)
	// 仅在「完全没有活动意图(open+running)」时才 kick 首轮规划。带种子意图的任务:种子已
	// 是 open,或已被上面刚起的 worker 抢先 claim 成 running——两种都算「有活干」,一律跳过
	// 首轮 planner,worker 直接领种子意图开跑,跑完由 NotifyDone/心跳唤醒 planner。
	// ⚠️ 不能用 Frontier(只数 open):worker 领取(open→running)与本检查存在竞态,会误 kick。
	// 重启自动恢复时也可能只剩 running 意图,同样应跳过。
	// 【二开·P-REVIEW·S4a】起 —— kick 判定并列 proposed（01 册 S4 操作 a）
	// 有待审意图（proposed+executor='review' 双条件，m00973/K24）时首轮不空 kick：
	// 待审意图等人放行/驳回后自然唤醒 planner，空 kick 只会让 planner 重复派单。
	if e.shouldHoldPlannerForUncontrolledAgent(t) {
		xlog.Oncef("planner-uncontrolled-hold-kick-"+t.ID, 5*time.Minute, xlog.LevelInfo,
			"[engine] task %s 首轮规划 kick 跳过：存在未完成的免规划自定义 intent", t.ID)
	} else if has, _ := t.Store.HasActiveIntent(); !has {
		if proposed, _ := t.Store.HasProposedIntent(); !proposed {
			t.Notify() // kick the first planning round (acted on once LLM is ready)
		} else {
			// 【二开·P-LOG·S4a】INFO 记 kick 跳过（排障：任务建好为什么不自己跑）
			log.Printf("[engine] task %s 首轮规划 kick 跳过：有待审意图（等人工放行/驳回后自然唤醒）", t.ID)
		}
	}
	// 【二开·P-REVIEW·S4a】止
}

func (e *Engine) startWorkerLoops(ctx context.Context, rt *taskRuntime, t *Task, workers int) {
	for i := range workers {
		name := fmt.Sprintf("work#%d", i+1)
		runTaskRoutine(rt, "worker "+name+" task="+t.ID, func(loopCtx context.Context) { e.workerLoop(loopCtx, t, name) })
	}
}

// runPreAgentStage 用对话型 Agent 执行首个种子，并在完成前保持普通 Worker 互斥。
// 可恢复失败保留现场并等待人工重跑；未领取退出时保持 proposed/open，不回落 Worker。
func (e *Engine) runPreAgentStage(ctx context.Context, t *Task) bool {
	for {
		if ctx.Err() != nil || e.IsDeleting(t.ID) || e.isSettling(t.ID) || isTerminalStatus(e.m.TaskStatus(t.ID)) {
			return false
		}
		if e.IsPaused(t.ID) {
			if sleepCtx(ctx, 800*time.Millisecond) {
				return false
			}
			continue
		}
		seed, err := t.Store.CustomSeedIntent(t.PreAgent)
		if err != nil {
			xlog.Oncef("pre-agent-seed-"+t.ID, 5*time.Minute, xlog.LevelError,
				"[executor] task %s 查询首个种子失败: %v", t.ID, err)
			if sleepCtx(ctx, 800*time.Millisecond) {
				return false
			}
			continue
		}
		if seed == nil || seed.State == "done" || seed.State == "deleted" {
			return true
		}
		switch seed.State {
		case "proposed", "open":
			if e.preAgentFn == nil {
				xlog.Errorf("[executor] task %s 没有 pre_agent 对话入口，保持种子 %s", t.ID, seed.State)
				return false
			}
			if !e.beginTaskOperation(t.ID) {
				return false
			}
			runCtx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false)
			claimed, runErr := func() (bool, error) {
				defer e.decInflight(t.ID)
				e.stampFirstRun(t)
				e.touch(t.ID)
				e.BeginLLMCall(t.ID)
				defer e.EndLLMCall(t.ID)
				return e.preAgentFn(runCtx, t)
			}()
			if runErr != nil {
				xlog.Oncef("pre-agent-conversation-"+t.ID, 5*time.Minute, xlog.LevelError,
					"[executor] task %s 首个意图对话执行失败: %v", t.ID, runErr)
			}
			if !claimed {
				if taskExecutionPaused(context.Cause(runCtx)) || e.IsPaused(t.ID) {
					if sleepCtx(ctx, 800*time.Millisecond) {
						return false
					}
					continue
				}
				latest, readErr := t.Store.CustomSeedIntent(t.PreAgent)
				if readErr != nil {
					xlog.Oncef("pre-agent-seed-"+t.ID, 5*time.Minute, xlog.LevelError,
						"[executor] task %s 对话后查询种子失败: %v", t.ID, readErr)
					if sleepCtx(ctx, 800*time.Millisecond) {
						return false
					}
					continue
				}
				if latest == nil || latest.State == "done" || latest.State == "deleted" {
					continue
				}
				// An unclaimed run with an error can be transient (PostgreSQL
				// unavailable, conversation creation failed). Back off and retry;
				// only a successful no-match run may end the pre-agent stage.
				if runErr != nil {
					if sleepCtx(ctx, 800*time.Millisecond) {
						return false
					}
					continue
				}
				return false
			}
			if runErr != nil && sleepCtx(ctx, 800*time.Millisecond) {
				return false
			}
		case "running", "paused", "blocked", "exhausted", "stopped":
			// 对话内 running/paused 等待其真实结束；失败态只由人工 rerun/delete 改变。
			if sleepCtx(ctx, 800*time.Millisecond) {
				return false
			}
		default:
			xlog.Errorf("[executor] task %s 首个种子状态 %q 不可执行", t.ID, seed.State)
			return false
		}
	}
}

func (e *Engine) hasOnTaskCreateTrigger(executorKey string) (bool, error) {
	if e.m == nil || e.m.pg == nil || executorKey == "" {
		return false, nil
	}
	a, err := e.m.pg.GetAgentByExecutorKey(executorKey)
	if err != nil || a == nil {
		return false, err
	}
	var enabled bool
	err = e.m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_triggers WHERE agent_key=$1 AND enabled=true AND on_task_create=true)`, a.Key).Scan(&enabled)
	return enabled, err
}

// plannerHeartbeatInterval 解析任务的 planner 心跳间隔。db.CreateTask 已归一
// (低于 600 一律抬到 600);这里再兜一次底,防内存态异常值。
func plannerHeartbeatInterval(t *Task) time.Duration {
	sec := t.PlanHeartbeatSeconds
	if sec < db.MinPlanHeartbeatSeconds { // 下限=默认=600(10min)
		sec = db.MinPlanHeartbeatSeconds
	}
	return time.Duration(sec) * time.Second
}

// shouldHoldPlannerForUncontrolledAgent 判断是否存在未完成且不受 Planner 控制的 custom intent。
// 判据来自 intent 的 executor_key 和 Agent 当前配置，适用于所有触发路径，而非仅首个 seed。
// 查询失败时关闭自动规划，避免异常时绕过契约。
func (e *Engine) shouldHoldPlannerForUncontrolledAgent(t *Task) bool {
	if t == nil || t.Store == nil {
		return false
	}
	hold, err := t.Store.HasPendingUncontrolledCustomIntent()
	if err != nil {
		xlog.Oncef("planner-uncontrolled-hold-error-"+t.ID, 5*time.Minute, xlog.LevelError,
			"[engine] task %s 不受 Planner 控制门控查询失败，暂停自动规划: %v", t.ID, err)
		return true
	}
	return hold
}

// resetPlannerTimer 安全重臂一个可能已触发的 Timer(标准 Stop→drain→Reset 模式)。
func resetPlannerTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func (e *Engine) plannerLoop(ctx context.Context, t *Task) {
	e.plannerLoopWithInterval(ctx, t, plannerHeartbeatInterval(t))
}

// plannerLoopWithInterval 保留生产心跳默认值，同时让回归测试能走过真实 heartbeat 分支。
func (e *Engine) plannerLoopWithInterval(ctx context.Context, t *Task, interval time.Duration) {
	// 心跳定时器在 loop 入口臂 = 从任务 start 计时:即使是跳过首轮 planner 的 seed 任务
	// (Run 里 frontier 非空不 kick 首轮)、这里一直阻塞,心跳也会在「任务 start + interval」
	// 触发第一轮规划。之后每次唤醒(边沿/心跳)都重臂 = 距上次任意规划触发的时长。
	heartbeat := time.NewTimer(interval)
	defer heartbeat.Stop()

	// runRound 跑一轮规划(含 debounce 合并 + 各 guard)。src 仅用于日志区分触发来源。
	runRound := func(src string) {
		// debounce: coalesce a burst of changes into one planning round
		timer := time.NewTimer(e.debounce)
	drain:
		for {
			select {
			case <-t.notify:
			case <-timer.C:
				break drain
			}
		}
		planner, _ := e.snapshotFor(t)
		if planner == nil {
			return // idle until LLM configured
		}
		if e.IsPaused(t.ID) {
			return // user-paused: don't plan
		}
		if e.IsDeleting(t.ID) {
			return
		}
		// terminal task (goals all met → done, or failed): the run is over. A
		// resume/nudge — e.g. auto-resume of the active task on restart — must NOT
		// re-plan (it would burn an LLM round and re-confirm a settled result).
		if isTerminalStatus(t.lifecycleSnapshot().Status) {
			return
		}
		// 任务级超时收尾中:丢弃普通唤醒——worker 收尾写回、Resume 的 Notify 都不再
		// 触发常规规划轮;终局那一轮由协调器(settleTask)直接驱动,不走这里。
		if e.isSettling(t.ID) {
			return
		}
		// 不受 Planner 控制的 custom intent 未完成时，edge/heartbeat 一律不进入 Planner。
		// 该守卫位于 goalless 之前，避免种子被误判为已跑干；真实 done 或人工删除后自动解除。
		if e.shouldHoldPlannerForUncontrolledAgent(t) {
			t.drainTriggersRetaining("revision_requested", "hint")
			xlog.Oncef("planner-uncontrolled-hold-"+t.ID, 5*time.Minute, xlog.LevelInfo,
				"[engine] task %s planner 规划轮抑制：存在未完成的免规划自定义 intent（%s，等待人工或真实完成）", t.ID, src)
			return
		}
		// 【二开·P-INTENT·S3】起 —— planner 心跳抑制守卫（01 册 S3）
		// 有待审意图（proposed ∧ executor='review'）时 planner 不再跑常规规划轮：
		// 决策权已移交人工，planner 续跑只会重复派单/干扰审核。人放行/驳回后经
		// NotifyDone 边沿唤醒，本守卫自动失效（待审清零）恢复正常规划。
		if proposed, err := t.Store.HasProposedIntent(); err == nil && proposed {
			// 【二开·P-LOG·S3】INFO 记抑制（排障：planner 为什么没跑）
			log.Printf("[engine] task %s planner 规划轮抑制：有待审意图（决策权在审核面板）", t.ID)
			// 【二开·审核流 PR5/§4.2.4】不再盲丢：revision_requested/hint 保留在队列，
			// 审核解除后随下一轮送达；done/finding 等易再生触发仍丢弃防无界累积。
			// 注意：NotifyHint 产生的事件 Kind 是 "hint"（manager.go NotifyHint），不是 "planner_hint"。
			t.drainTriggersRetaining("revision_requested", "hint")
			return
		}
		// 【二开·P-INTENT·S3】止
		// goalless（人工直投）分支：任务已无 open 目标时 planner 不跑——跑了会重判
		// met→cancelExec 杀掉用户经主 agent 直投的意图。是否结束改由 frontier 决定：
		// 还有 open/running 意图 → 保持 running、静默等待；意图已全部跑干 → 落 done。
		// 整段纯 Go、不触发任何 LLM 调用，也不打规划轮 marker。
		// 【二开·P-INTENT·S4】起 —— goalless 双改（01 册 S4）
		// 有待审意图时不进 goalless 收尾：frontier 只认 open，proposed 意图会被
		// HasActiveIntent 漏算，若不豁免 → 任务被误判"跑干"落 done，待审意图烂在库里。
		if proposed, err := t.Store.HasProposedIntent(); err == nil && proposed {
			// 【二开·P-LOG·S4b】INFO 记 goalless 豁免（排障：任务为什么一直 running 不收官）
			log.Printf("[engine] task %s goalless 收尾豁免：有待审意图，保持 running 等待人工审核", t.ID)
			return // 保持 running，静默等待人工审核（同样不触发 LLM）
		}
		// 【二开·P-INTENT·S4】止
		// 【二开·审核流 PR5/§4.2.4】rejected 例外：无 open 目标但存在被驳回待修订意图时，
		// 放行 planner 轮（revision_requested 事件需要 planner 用 update_intent 修订原稿重送），
		// 否则 goalless 分支永远提前 return，修订永远无人做。
		hasRejected, rjErr := t.Store.HasRejectedIntent()
		if open, err := t.Store.HasOpenGoal(); err == nil && !open && !(rjErr == nil && hasRejected) {
			t.drainTriggers() // 丢弃累积的 done/finding 触发，避免 goalless 长会话里无界增长
			if active, err := t.Store.HasActiveIntent(); err == nil && !active {
				// frontier 抽干且无在跑意图 → 收尾。用 Guarded 版做 CAS，避免踩到并发的
				// pause/delete/超时收尾的状态转换。
				if won, err := e.m.SetTaskStatusGuarded(t.ID, "done"); err != nil {
					log.Printf("[goalless] task %s 收尾落 done 失败: %v", t.ID, err)
				} else if won {
					e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
						Summary: "目标已全部达成，直投意图已执行完毕，任务结束"})
				}
			}
			return // goalless 分支永不进入 planner.Plan
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		defer e.decInflight(t.ID)
		e.stampFirstRun(t) // 首次真正规划 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
		e.touch(t.ID)
		emit := func(r db.Activity) { e.emitActivity(t, r) }
		ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
		if ectx.Err() != nil || e.IsDeleting(t.ID) {
			return
		}
		log.Printf("[planner] task %s 规划中…(%s 触发)", t.ID, src)
		// round marker: each Plan() is one planner round; emit a boundary so the
		// UI can separate rounds in the transcript (kind='round').
		e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
			Summary: fmt.Sprintf("第 %d 轮规划", e.nextPlannerRound(t.ID))})
		// what fired this round (worker done / finding; may be several — debounce
		// coalesces a burst; empty for time/heartbeat wakes).
		triggers := t.drainTriggers()
		taskIDInt, _ := strconv.ParseInt(t.ID, 10, 64)
		e.BeginLLMCall(t.ID)
		met, reason, err := planner.Plan(ectx, taskIDInt, e.m.assets, t.Store, t.Goal, triggers, emit)
		e.EndLLMCall(t.ID)
		switch {
		case err != nil && ectx.Err() == nil:
			log.Printf("[planner] task %s 规划出错: %v", t.ID, err)
		case met:
			log.Printf("[planner] task %s 判定目标达成: %s", t.ID, reason)
			if rj, rerr := t.Store.HasRejectedIntent(); rerr == nil && rj {
				// 【二开·审核流 PR5/§4.2.4】goal_met 守卫：存在被驳回待修订意图时不许落 done——
				// 修订还没做完，任务保持 running 等 planner 处理 revision_requested。
				log.Printf("[planner] task %s 目标达成判定被拦截：仍有被驳回待修订意图", t.ID)
			} else {
				// 所有目标达成 → 持久化任务状态为 done（前端 DTO 会优先展示该终态）。
				if err := e.m.SetTaskStatus(t.ID, "done"); err != nil {
					log.Printf("[planner] task %s 标记完成落库失败: %v", t.ID, err)
				}
				// 任务已判完成 → 立刻取消在跑的 worker：它们手头的意图跑出来也没意义了。
				// 下一轮 worker 循环撞终态门就不再领新意图;被取消的这批走下方"任务已完成"分支
				// 归为 stopped(而非 blocked)。
				e.cancelExec(t.ID, agent.AbortGoalMet)
			}
		default:
			log.Printf("[planner] task %s 规划完成", t.ID)
		}
		e.touch(t.ID)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.notify:
			runRound("edge") // worker 结束 / finding / kill / resume / seed 首轮
		case <-heartbeat.C:
			// 【二开·P-REVIEW·S3】起 —— 心跳抑制守卫：只有待审意图、没有活跃意图时，planner 心跳空转无意义
			// （产了意图也堆在待审/或目标已被人为接管），跳过本轮心跳。edge 触发的 runRound 不受影响。
			if active, err := t.Store.HasActiveIntent(); err == nil && !active {
				if proposed, err := t.Store.HasProposedIntent(); err == nil && proposed {
					// 【二开·P-LOG·S3】INFO 记心跳抑制（排障：planner 心跳为什么停了）
					log.Printf("[engine] task %s planner 心跳抑制：仅待审意图无活跃意图", t.ID)
					resetPlannerTimer(heartbeat, interval)
					continue
				}
			}
			// 【二开·P-REVIEW·S3】止
			// custom proposed 等待执行器领取时，planner 心跳同样无意义——执行完成后会 edge 唤醒。
			if assigned, err := t.Store.HasAssignedIntent(); err == nil && assigned {
				log.Printf("[engine] task %s planner 心跳抑制：有自定义执行器待领取意图", t.ID)
				resetPlannerTimer(heartbeat, interval)
				continue
			}
			// 周期兜底:死锁兜底 + 唤醒去监督飞行中的 worker(steer/kill) + 周期复查。
			runRound("heartbeat")
		}
		// 每次唤醒(边沿或心跳)后重臂心跳:任意规划触发都重算这段静置计时。
		resetPlannerTimer(heartbeat, interval)
	}
}

func (e *Engine) workerLoop(ctx context.Context, t *Task, name string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, worker := e.snapshotFor(t)
		if e.IsPaused(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // user-paused: don't claim/execute intents
		}
		if e.IsDeleting(t.ID) {
			return
		}
		if e.isSettling(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 任务超时收尾中:不再领新意图(在跑的自行收尾,协调器等其 drain)
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 任务已终态(done/failed/timeout):停止领取遗留意图,别在完成后空跑 frontier
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		claimed := e.runWorkerStep(ctx, t, name, worker)
		e.decInflight(t.ID)
		if !claimed && sleepCtx(ctx, 800*time.Millisecond) {
			return
		}
	}
}

// 【二开·P-EXEC·S2 executor 三层校验】起 —— 按 executor_key 解析自定义执行器 agent：
// 不存在/已禁用/内置 agent 三层校验，失败回退默认 worker（R6）。

// resolveExecutor 按 executor_key 找到唯一启用、非内置的 agent 配置。
// 返回错误时调用方负责回退默认 worker（pickExecutor 的 R6 分支）。
func (e *Engine) resolveExecutor(key string) (*db.Agent, error) {
	if key == "" {
		return nil, fmt.Errorf("executor_key 为空")
	}
	a, err := e.m.pg.GetAgentByExecutorKey(key) // D10 新增
	if err != nil {
		return nil, fmt.Errorf("查询执行器失败: %w", err)
	}
	if a == nil {
		return nil, fmt.Errorf("执行器标识 %q 未注册", key)
	}
	if !a.Enabled {
		return nil, fmt.Errorf("执行器 %q 所属 agent 已禁用", key)
	}
	if a.Builtin {
		return nil, fmt.Errorf("内置 agent 不能作为自定义执行器（executor_key 只面向自定义 agent）")
	}
	return a, nil
}

// llmProfileOrZero：providerForProfile 参数是值类型（见 server.go#providerForProfile 签名）——nil 绑定时传 0 走全局激活配置。
func llmProfileOrZero(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// executorKeyForIntent 从意图 payload 解析自定义执行器 key。
// 返回空字符串表示：无 executor、JSON 解析失败、或 executor='review'（人工审核占位符）。
// 该函数是 runIntent 路由判断的唯一入口，确保 review 不会被误路由到自定义执行器。
func executorKeyForIntent(intent *db.Node) string {
	var p map[string]any
	if json.Unmarshal(intent.Payload, &p) != nil {
		return ""
	}
	if ex, ok := p["executor"].(string); ok && ex != "review" {
		return ex
	}
	return ""
}

// isUncontrolledByPlanner 按 intent 的 executor_key 读取 Agent 当前配置。
// false 只代表该 Agent 的所有 custom intent 不受 Planner 控制；Agent 缺失或查询失败时保持原有 Planner 行为。
func (e *Engine) isUncontrolledByPlanner(intent *db.Node) bool {
	executorKey := executorKeyForIntent(intent)
	if executorKey == "" || e.m == nil || e.m.pg == nil {
		return false
	}
	a, err := e.m.pg.GetAgentByExecutorKey(executorKey)
	return err == nil && a != nil && !a.PlannerControlled
}

// queuedIntentState 决定意图恢复待执行时应回 proposed 还是 open。
// 判断依据：原状态为 proposed 或含有效自定义 executor 时回 proposed，其余回 open。
// exec:<unassigned> 因原状态 proposed 也回 proposed。
// 该函数保证 custom 意图在暂停/恢复/重跑后始终回到 proposed，由 exec:<key> 重新领取，
// 不会被默认 worker 通过 Frontier 错误领取。
func queuedIntentState(intent *db.Node) string {
	if intent.State == "proposed" || executorKeyForIntent(intent) != "" {
		return "proposed"
	}
	return "open"
}

// terminalStateForIntent 将 runner 终止原因映射为 intent 状态。
// custom 只有 completed 可以 done；其他未知或空 reason 一律按未完成处理，禁止假完成。
// 普通 Worker 保留既有 completed/max_turns/timeout 语义。
func terminalStateForIntent(customRouted bool, reason harness.TerminalReason, runErr error) string {
	if runErr != nil {
		return "blocked"
	}
	if customRouted {
		switch reason {
		case harness.ReasonCompleted:
			return "done"
		case harness.ReasonMaxTurns, harness.ReasonTimeout:
			return "exhausted"
		case harness.ReasonHookStopped, harness.ReasonStopHookPrevented,
			harness.ReasonAbortedStreaming, harness.ReasonAbortedTools:
			return "stopped"
		default:
			return "blocked"
		}
	}
	if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		return "exhausted"
	}
	return "done"
}

// runCustomExecutorSafely 包装 agent.RunCustomExecutor，提供安全边界：
//   - nil Agent：返回明确错误（自定义执行器 agent 为空），boundaryError=true
//   - nil Provider：返回明确错误（没有可用的 LLM 配置），boundaryError=true
//   - 同步 panic：recover 捕获并记录 debug.Stack() 堆栈，boundaryError=true
//   - 正常返回及普通 Provider 错误：原样透传，boundaryError=false
//
// boundaryError=true 时由 runIntent 用 taskEmit 写一条 Kind=result, IsError=true 的活动记录，
// 然后快速进入 blocked 终态，不再进入 model_error 重试循环。
// 该包装确保 nil Provider/panic 不会跳过 EndLLMCall、detachWork、completeWork 和 inflight 清理。
func runCustomExecutorSafely(ctx context.Context, cfg agent.CustomExecutorConfig) (reason harness.TerminalReason, wrote agent.WriteCounts, err error, boundaryError bool) {
	if cfg.Agent == nil {
		return reason, wrote, fmt.Errorf("自定义执行器 agent 为空"), true
	}
	if cfg.Provider == nil {
		return reason, wrote, fmt.Errorf("没有可用的 LLM 配置"), true
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[executor] 自定义执行器 panic: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("自定义执行器 panic: %v", r)
			boundaryError = true
		}
	}()
	reason, wrote, err = agent.RunCustomExecutor(ctx, cfg)
	return reason, wrote, err, false
}

// CanDispatch 判断任务是否可以分派执行器工作：
//   - ReadyFor(t)=true：默认 planner/worker LLM 已就绪，走原逻辑。
//   - HasExecutorDispatchWork()=true：存在 custom open/proposed/running 工作，
//     即使默认 worker 不可用（无全局 LLM），也应允许任务启动并占一个并发槽。
//
// 该函数用于替代 ReadyFor 做准入判断，避免 custom proposed 在无默认 LLM 时被永久排队。
// llm_configured 语义保持不变，仍由 ReadyFor 决定。
func (e *Engine) CanDispatch(t *Task) bool {
	if e.ReadyFor(t) {
		return true
	}
	ok, err := t.Store.HasExecutorDispatchWork()
	return err == nil && ok
}

// 【二开·P-EXEC·S2 executor 三层校验】止

// runWorkerStep claims one intent from the dispatch frontier and fully settles it via
// runIntent. Returns false when nothing was claimable. The pool worker loop is its
// only caller.
func (e *Engine) runWorkerStep(ctx context.Context, t *Task, name string, worker *agent.Worker) bool {
	intent, executionName, customPreclaimed := e.claimNext(t, name, worker)
	if intent == nil {
		return false
	}
	log.Printf("[worker %s] task %s 领取意图 #%d (执行者 %s)", executionName, t.ID, intent.ID, executionName)
	return e.runIntent(ctx, t, executionName, worker, intent, "", "", customPreclaimed)
}

// runIntent executes and fully settles one already-claimed (state=running) intent.
// Both the pool worker loop (via runWorkerStep) and the human-message handler (via
// runDetachedIntent, a dedicated goroutine outside the worker pool) call it, so the
// execute/retry/state-write logic lives in exactly one place. A non-empty message
// is injected as this turn's input through ExecuteWithMessage; requestID keys the
// transcript marker that dedups re-injection across model_error retries. The caller
// must already hold one task-operation admission for the whole sequence so a delete
// cannot observe quiescence between the LLM return and the final DB writes.
func (e *Engine) runIntent(ctx context.Context, t *Task, name string, worker *agent.Worker, intent *db.Node, requestID, message string, customPreclaimed bool) bool {
	hasChatMessage := message != ""
	e.stampFirstRun(t) // 首次真正执行 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
	e.touch(t.ID)
	emit := func(r db.Activity) { e.emitActivity(t, r) }
	ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
	if ectx.Err() != nil || e.IsDeleting(t.ID) {
		targetState := queuedIntentState(intent)
		if err := transitionIntentState(t.Store, intent.ID, targetState); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 领取后回退失败: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// per-work child context so the planner's kill_work can stop just this work.
	workCtx, workCancel := context.WithCancelCause(ectx)
	e.registerWork(intent.ID, workCancel)
	// wrap the guard hooks so steer_work can inject a mid-run course-correction
	// for THIS intent (drained before the worker's next tool call).
	iid := intent.ID
	taskEmit := func(a db.Activity) {
		nid := iid
		a.NodeID, a.Worker = &nid, name
		emit(a)
	}
	label := fmt.Sprintf("%s · #%d", name, iid)
	workCtx = intercept.WithTaskContext(workCtx, t.ID, label, taskEmit)
	// nudges 有意建在 model_error 重跑循环之外:空转续跑的上限是「这条意图」的总量，
	// 重跑一轮不该把额度清零重来。
	hooks := steerHooks{
		inner:  t.Guard.Hooks(),
		drain:  func() (string, bool) { return e.drainSteer(iid) },
		nudges: &atomic.Int64{},
		limit:  e.emptyTurnNudgeLimit(),
		label:  label,
	}
	wTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	var reason harness.TerminalReason
	var wrote agent.WriteCounts
	var err error
	// 【二开·P-EXEC·S1 pickExecutor 分流】起 —— payload.executor 指定的自定义执行器取代默认 worker
	// 规则：payload.executor 非空 且 本次不是「人发消息续跑」（message 为空）→ 解析并走 RunCustomExecutor；
	// 人发消息续跑强制默认 worker（对话上下文只存在于 worker 会话，自定义执行器无会话态）。
	executorKey := executorKeyForIntent(intent)
	routed := false
	customRouted := false
	var customCfg agent.CustomExecutorConfig
	if executorKey != "" && !hasChatMessage {
		if ea, resolveErr := e.resolveExecutor(executorKey); resolveErr != nil {
			if intent.State == "proposed" {
				// custom proposed 的 executor 不存在/禁用/内置时直接 blocked，不回退默认 Worker。
				e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
					Summary: fmt.Sprintf("意图 #%d 指定执行器 %q 不可用（%v），无法执行", intent.ID, executorKey, resolveErr)})
				log.Printf("[engine] task %s 意图 #%d custom proposed executor %q 解析失败（%v），标记 blocked", t.ID, intent.ID, executorKey, resolveErr)
				if stateErr := transitionIntentState(t.Store, intent.ID, "blocked"); stateErr != nil {
					log.Printf("[worker %s] task %s 意图 #%d blocked 落库失败: %v", name, t.ID, intent.ID, stateErr)
				}
				return true
			}
			// 存量 open custom 保留 R6 默认 Worker 回退。
			e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
				Summary: fmt.Sprintf("意图 #%d 指定执行器 %q 不可用（%v），已回退默认 worker", intent.ID, executorKey, resolveErr)})
			log.Printf("[P-INTENT·S1] WARN task %s 意图 #%d executor %q 解析失败（%v），回退默认 worker", t.ID, intent.ID, executorKey, resolveErr)
		} else {
			log.Printf("[engine] executor 路由命中：task %s 意图 #%d executor=%s（pre_agent 编排路由）", t.ID, intent.ID, executorKey)
			var prov llm.Provider
			if e.providerFn != nil {
				prov, _, _ = e.providerFn(llmProfileOrZero(ea.LLMProfileID))
			}
			customCfg = agent.CustomExecutorConfig{
				Agent: ea, Provider: prov, TaskID: wTaskID, Intent: intent,
				Assets: e.m.assets, Store: t.Store, Goal: t.Goal,
				Hooks: hooks, Emit: emit, Enrich: e.m.enrich,
				NotifyFinding: t.NotifyFinding, RequireSelfClaim: !customPreclaimed,
				WorkDir: e.m.dir, ProxyAddr: e.m.ProxyAddr(), ProxyCACert: e.m.ProxyCACert(),
			}
			routed = true
			customRouted = true
		}
	}
	if intent.State == "proposed" && executorKey == "" && !hasChatMessage {
		// exec:<unassigned> 对应的 proposed 空 executor：直接写错误并 blocked。
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("意图 #%d 未配置 executor，无法执行", intent.ID)})
		log.Printf("[engine] task %s 意图 #%d proposed 空 executor，标记 blocked", t.ID, intent.ID)
		if stateErr := transitionIntentState(t.Store, intent.ID, "blocked"); stateErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d blocked 落库失败: %v", name, t.ID, intent.ID, stateErr)
		}
		return true
	}
	// 【二开·P-EXEC·S1 pickExecutor 分流】止
	if !routed && worker == nil {
		// 默认 worker 尚未就绪，禁止 nil 解引用；覆盖 open custom 的 R6 回退和 hasChatMessage 分支。
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("意图 #%d 默认 worker 尚未就绪，无法执行", intent.ID)})
		log.Printf("[engine] task %s 意图 #%d 默认 worker 尚未就绪，标记 blocked", t.ID, intent.ID)
		if stateErr := transitionIntentState(t.Store, intent.ID, "blocked"); stateErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d blocked 落库失败: %v", name, t.ID, intent.ID, stateErr)
		}
		return true
	}
	var boundaryError bool
	if customRouted {
		reason, wrote, err, boundaryError = runCustomExecutorSafely(workCtx, customCfg)
	} else if hasChatMessage {
		reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding, requestID, message)
	} else {
		reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding)
	}
	if boundaryError {
		// 安全边界错误（nil Agent/Provider/panic）：写一条错误结果，快速进入 blocked。
		taskEmit(db.Activity{Kind: "result", IsError: true, Summary: err.Error()})
	}
	e.EndLLMCall(t.ID)
	// model_error 收场 → 额外重跑几次（退避后再试）。仅在意图仍属本 work、任务
	// 未暂停/未终止/未取消【且未进入收尾】时重试；否则让位给对应分支处理(收尾期不
	// 再重试,避免退避挤占其他 worker 的优雅收尾窗口)。
	maxRetries, retryBackoff := e.modelErrorRetryPolicy()
	for attempt := 1; attempt <= maxRetries &&
		retryableWorkerModelError(reason, err) && !boundaryError &&
		workCtx.Err() == nil && ectx.Err() == nil && !e.IsPaused(t.ID) && !e.isSettling(t.ID); attempt++ {
		log.Printf("[worker %s] task %s 意图 #%d model_error 收场，%v 后重试 (%d/%d)",
			name, t.ID, intent.ID, retryBackoff, attempt, maxRetries)
		if sleepCtx(workCtx, retryBackoff) {
			break // 退避期间被取消（终止/暂停）→ 交给下方分支处理
		}
		e.BeginLLMCall(t.ID)
		if customRouted {
			reason, wrote, err, boundaryError = runCustomExecutorSafely(workCtx, customCfg)
		} else if hasChatMessage {
			reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding, requestID, message)
		} else {
			reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding)
		}
		if boundaryError {
			taskEmit(db.Activity{Kind: "result", IsError: true, Summary: err.Error()})
		}
		e.EndLLMCall(t.ID)
	}
	// Capture kill state before detachWork cancels workCtx. kill = this work's
	// ctx was cancelled (planner kill_work) while the TASK ctx kept running; a
	// pause cancels the task ctx (ectx) instead. Checking workCtx.Err() AFTER
	// unregister would always be true (unregister cancels it) → every completed
	// work would be wrongly marked stopped.
	workCause := context.Cause(workCtx)
	killed := workCtx.Err() != nil && ectx.Err() == nil
	action, completeWork := e.detachWork(intent.ID)
	// A caller may stop waiting and release its in-memory reservation before the
	// agent honors cancellation. The named context cause remains authoritative and
	// still settles the stopped run into a recoverable state.
	if action == "" {
		switch {
		case errors.Is(workCause, agent.AbortWorkPausedByUser):
			action = "pause"
		case errors.Is(workCause, agent.AbortWorkCancelledByUser):
			action = "cancel"
		}
	}
	var controlErr error
	defer func() { completeWork(controlErr) }()
	if errors.Is(err, agent.ErrNoMatchingIntent) {
		log.Printf("[executor %s] task %s 无匹配 intent #%d，保持 %s 并退出，不启动默认 Worker", name, t.ID, intent.ID, intent.State)
		return false
	}
	if action == "pause" {
		controlErr = transitionIntentState(t.Store, intent.ID, "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 暂停状态落库失败: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 意图 #%d 已暂停", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	if action == "cancel" {
		// Park the stopped run in paused before handing cleanup to the API. If the
		// request disconnects after cancellation, the intent remains recoverable and
		// a later cancel can finish cleanup instead of leaving a phantom running row.
		controlErr = transitionIntentState(t.Store, intent.ID, "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 取消栅栏落库失败: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 意图 #%d 已停止，等待取消清理", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	// if a pause cancelled this run mid-flight, return the intent to the frontier
	// so it is re-claimed on resume — the worker will resume the prior LLM
	// conversation from its transcript instead of restarting from scratch.
	if ectx.Err() != nil && taskExecutionPaused(context.Cause(ectx)) {
		targetState := queuedIntentState(intent)
		if err := transitionIntentState(t.Store, intent.ID, targetState); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 任务暂停回退失败: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// 任务超时收尾的硬兜底 cancel(非 pause、非 kill)取消了本 run → 归为 exhausted(已收尾),
	// 不要误标 blocked。此时 worker 通常已在 settlement 阶段把结果写回。
	if ectx.Err() != nil && e.isSettling(t.ID) {
		if err := transitionIntentState(t.Store, intent.ID, "exhausted"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 超时收尾状态落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 因任务超时收尾结束(exhausted)，写回 %s", name, t.ID, intent.ID, wrote)
		e.touch(t.ID)
		return true
	}
	// 任务已判完成(done via 常规路径)→ 上面 cancelExec 取消了本 run。意图结果已无意义,
	// 标 stopped(不是 blocked),别污染已完成任务的意图状态。
	if ectx.Err() != nil && isTerminalStatus(e.m.TaskStatus(t.ID)) {
		if err := transitionIntentState(t.Store, intent.ID, "stopped"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 终态停止落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 因任务已完成而取消(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	// killed by the planner: mark stopped (don't write back results, don't auto-reclaim).
	if killed {
		if err := transitionIntentState(t.Store, intent.ID, "stopped"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d planner 停止落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 被终止(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		if !e.isUncontrolledByPlanner(intent) {
			t.Notify()
		}
		return true
	}
	if err != nil {
		log.Printf("[worker %s] intent %d: %v", name, intent.ID, err)
	}
	// terminal分流：custom 只有 completed 才算完成；max_turns/timeout 仍为 exhausted。
	// 非 completed 的 hook/abort 和未知 reason 不再落入默认 done。
	state := terminalStateForIntent(customRouted, reason, err)
	switch reason {
	case harness.ReasonMaxTurns:
		log.Printf("[worker %s] intent %d 撞步数上限(exhausted)，本次写回 %s", name, intent.ID, wrote)
	case harness.ReasonTimeout:
		log.Printf("[worker %s] intent %d 运行超时(exhausted)，收尾后写回 %s", name, intent.ID, wrote)
	}
	if customRouted && err == nil && state == "blocked" {
		log.Printf("[worker %s] intent %d 终止原因 %q 不是 completed，按未完成 blocked 处理", name, intent.ID, reason)
	}
	if state == "blocked" && isTaskLLMChainExhausted(err) {
		_ = t.Store.SetIntentBlockedReason(intent.ID, db.IntentBlockedLLMQuota)
	} else {
		if stateErr := transitionIntentState(t.Store, intent.ID, state); stateErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 终态 %s 落库失败: %v", name, t.ID, intent.ID, state, stateErr)
		}
	}
	log.Printf("[worker %s] task %s 意图 #%d 结束: %s (写回 %s)", name, t.ID, intent.ID, state, wrote)
	e.touch(t.ID)
	// 不受 Planner 控制的 intent 只有真实 done 才唤醒 Planner；其他 intent 保留原有失败重规划。
	if state == "done" || !e.isUncontrolledByPlanner(intent) {
		t.NotifyDone(intent.ID)
	}
	return true
}

// runDetachedIntent runs one paused intent OUTSIDE the worker pool in its own
// goroutine — the human-message path. It transitions the intent paused->running
// itself (never through 'open'), so the pool, which only claims 'open', can never
// race it; the "at most one run per intent" invariant still holds because winning
// the CAS is the sole entry and work[intentID] was cleared when the pause settled.
// Because it does not compete for a frontier slot, a user message continues the
// worker immediately even when all pool slots are busy (mirroring how the
// main-agent chat handler starts its run directly). The spawned goroutine owns one
// task-operation admission for the whole run and roots its context at ctx (pass the
// server root, never the HTTP request, so a disconnect cannot strand the run while
// task pause/delete/shutdown still stops it). Returns an error if the run could not
// be started; the intent is left untouched in that case.
func (e *Engine) runDetachedIntent(ctx context.Context, t *Task, intentID int64, requestID, message, agentMessage string) error {
	if !e.beginTaskOperation(t.ID) {
		return fmt.Errorf("task is being deleted")
	}
	release := true
	defer func() {
		if release {
			e.decInflight(t.ID)
		}
	}()
	_, worker := e.snapshotFor(t)
	if worker == nil {
		return fmt.Errorf("worker 尚未就绪")
	}
	node, err := t.Store.GetNode(intentID)
	if err != nil {
		return err
	}
	if node == nil || node.Kind != db.KindIntent {
		return fmt.Errorf("intent not found")
	}
	changed, err := t.Store.CompareAndSetIntentState(intentID, "paused", "running")
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: 意图不再是 paused 状态", db.ErrIntentStateConflict)
	}
	node.State, node.Owner = "running", "chat"
	// Record the human turn as a visible activity BEFORE the run starts, so it is
	// ordered ahead of any worker step and never appears without the run happening.
	// Keep the UI copy concise; ExecuteWithMessage writes the server-resolved
	// reference snapshot into the intent transcript as the LLM input.
	uid := intentID
	e.emitActivity(t, db.Activity{NodeID: &uid, Worker: "user", Kind: "user", Summary: message, Detail: message})
	release = false // ownership of the admission passes to the goroutine
	go func() {
		defer e.decInflight(t.ID)
		e.runIntent(ctx, t, "chat", worker, node, requestID, agentMessage, false)
	}()
	return nil
}

func taskExecutionPaused(cause error) bool {
	var abort *agent.AbortCause
	if !errors.As(cause, &abort) {
		return false
	}
	switch abort.Code {
	case "paused_by_user", "paused_by_orchestrator", "paused_on_reload", "paused_race_guard",
		"queued_for_admission", "llm_unavailable_queued", "task_deleted":
		return true
	default:
		return false
	}
}

func sleepCtx(ctx context.Context, d time.Duration) (done bool) {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

// claimNext 从分派前沿领取下一条可执行意图，返回意图、执行者和 custom 是否已预领取。
// 第三个返回值仅在 custom CAS 成功时为 true；普通 Worker、空 executor 和失败路径均为 false。
func (e *Engine) claimNext(t *Task, name string, worker *agent.Worker) (*db.Node, string, bool) {
	fr, err := t.Store.DispatchFrontier(20)
	if err != nil {
		xlog.Oncef("claimNext-frontier-"+t.ID, 5*time.Minute, xlog.LevelError,
			"[engine] claimNext task=%s frontier: %v", t.ID, err)
		return nil, "", false
	}
	for _, in := range fr {
		executorKey := executorKeyForIntent(in)
		if executorKey != "" {
			// 自定义执行器：open 和 proposed 都走此路径，兼容现存量 open custom 与审核批准后的 open custom。
			owner := "exec:" + executorKey
			ok, err := t.Store.ClaimExecutorIntent(in.ID, in.State, executorKey, owner)
			if err != nil {
				xlog.Oncef("claimNext-claim-exec-"+t.ID, 5*time.Minute, xlog.LevelError,
					"[engine] claimNext task=%s intent=%d executor=%s: %v", t.ID, in.ID, executorKey, err)
				continue
			}
			if ok {
				return in, owner, true
			}
			continue
		}
		if in.State == "proposed" {
			// proposed 且 executor 为空：owner 固定 exec:<unassigned>，由 runIntent 直接 blocked。
			ok, err := t.Store.ClaimExecutorIntent(in.ID, in.State, "", "exec:<unassigned>")
			if err != nil {
				xlog.Oncef("claimNext-claim-unassigned-"+t.ID, 5*time.Minute, xlog.LevelError,
					"[engine] claimNext task=%s intent=%d unassigned: %v", t.ID, in.ID, err)
				continue
			}
			if ok {
				return in, "exec:<unassigned>", false
			}
			continue
		}
		// 普通 open：worker 为 nil 时跳过并保持 open。
		if worker == nil {
			continue
		}
		ok, err := t.Store.ClaimIntent(in.ID, name)
		if err != nil {
			xlog.Oncef("claimNext-claim-"+t.ID, 5*time.Minute, xlog.LevelError,
				"[engine] claimNext task=%s intent=%d: %v", t.ID, in.ID, err)
			continue
		}
		if ok {
			return in, name, false
		}
	}
	return nil, "", false
}
