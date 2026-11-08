// customexec.go：意图级自定义执行器执行链——用声明 executor_key 的自定义 agent 跑一条意图。
// 主要函数/类型：
//   - CustomExecutorConfig：自定义执行器输入（agent/provider/意图/工具回调/工作目录等）。
//   - RunCustomExecutor：装配工具与系统提示词执行意图；骨架复刻 agent/worker.go#Worker.Execute（K18/K23），
//     差异只在系统提示词（自定义 agent 模板）、工具（WorkerTools 底座 + agent augment）与意图块（RenderIntentTask）。

package agent

import (
	"context"
	"errors"
	"fmt"
	"log" // 【二开·P-LOG·A2】INFO/WARN 级别直接用 log
	"time"

	"artex/db"
	"artex/intercept"
	"norma/harness"
	"norma/llm"
)

// CustomExecutorConfig 自定义执行器输入。Provider 由 engine 侧按 Agent.LLMProfileID 解析注入（S1 Provider 注）。
type CustomExecutorConfig struct {
	Agent    *db.Agent // S2 resolveExecutor 三层校验后的自定义 agent
	Provider llm.Provider
	TaskID   int64
	Intent   *db.Node
	Assets   *db.AssetStore
	Store    *db.ExplorationStore
	Goal     string
	Hooks    harness.HookRunner
	Emit     func(db.Activity)
	// Enrich/NotifyFinding：tsx.SetEnrich / tsx.SetNotifyFinding 由 ToolSet 内部消费——
	// finding 落库回填与 DNS/HTTP 异步补全均在 ToolSet 层完成（与 worker 同一回调链路），A2 无需自调。
	Enrich        EnrichTrigger
	NotifyFinding func(int64, string)
	WorkDir       string
	ProxyAddr     string
	ProxyCACert   string

	// RequireSelfClaim=true：Agent 自行 list/claim；false：Engine 已预领取，直接执行。
	RequireSelfClaim bool
}

// ErrNoMatchingIntent 表示当前自定义执行器没有可领取的 proposed/open intent。
// Engine 收到该错误时保留原状态并停止 custom 阶段，不启动默认 Worker。
var ErrNoMatchingIntent = errors.New("没有匹配的自定义执行器意图")

// 【二开·P-EXEC·A2 自定义执行器】起 —— 意图级自定义执行器：装配工具与提示词执行一条意图（K18/K23）。
// RunCustomExecutor 用声明 executor_key 的自定义 agent 执行一条意图。
// 与 worker.execute() 的差异只有三处（其余逐行对齐）：
//  1. 系统提示词：renderSystem(cfg.Agent.Key, "", vars)——模板从该 agent 的提示词覆盖链取；
//  2. 意图任务块：RenderIntentTask（A1 导出，与 worker 同文案）；
//  3. ToolSet.worker 名 = "exec:"+cfg.Agent.ExecutorKey（活动流可追溯是哪个执行器跑的）。
//
// 返回 (终态原因, 写入统计, 错误)，与 worker.Execute 同签名语义，runIntent 终态分流零改动。
func RunCustomExecutor(ctx context.Context, cfg CustomExecutorConfig) (harness.TerminalReason, WriteCounts, error) {
	if cfg.Agent == nil {
		return "", WriteCounts{}, errors.New("自定义执行器 Agent 不能为空")
	}
	executorKey := cfg.Agent.ExecutorKey
	if executorKey == "" {
		return "", WriteCounts{}, errors.New("自定义执行器 Agent.ExecutorKey 不能为空")
	}
	name := "exec:" + executorKey
	tsx := NewToolSet(cfg.Store, name)
	tsx.SetTaskID(cfg.TaskID)
	coverageEnabled := cfg.Assets == nil || cfg.Assets.CoverageEnabled(cfg.TaskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if cfg.Assets != nil {
		tsx.SetAssetStore(cfg.Assets, cfg.Assets.Companies())
	}
	tsx.SetOwnerNode(cfg.Intent.ID)         // 发现的资产锚定到本意图 → 任务可见
	tsx.SetEnrich(cfg.Enrich)               // DNS/HTTP 异步补全
	tsx.SetNotifyFinding(cfg.NotifyFinding) // report_finding 落库当场唤醒 planner
	// 工具底座：WorkerTools + list_intents + claim_executor_intent + 默认工具族 + agent augment。
	base := append(tsx.CustomExecutorTools(), defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: cfg.TaskID, ExplorationID: explorationID(cfg.Store), IntentID: cfg.Intent.ID})
	tools, def, cleanup := AugmentTools(ctx, cfg.Agent.Key, base)
	defer cleanup()
	// 本意图的专属工作目录 <workDir>/tasks/<taskID>/i<intentID>（与 worker 同款）。
	runDir := ensureRunDir(cfg.WorkDir, cfg.TaskID, cfg.Intent.ID)
	// 动作审核上下文：工作目录即会话归属（与 worker 同机制；不继承父 run 的 background）。
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	// 【二开·P-LOG·A2】启动日志：INFO 记开始执行（排障：这个意图是谁在跑）。
	log.Printf("[executor] 自定义执行器启动：task %d 意图 #%d agent=%s executor=%s", cfg.TaskID, cfg.Intent.ID, cfg.Agent.Name, name)
	// 系统提示词：该自定义 agent 的模板（renderSystem 走提示词覆盖链）+ 意图块（A1）+ 启动指令。
	sysBody := renderSystem(cfg.Agent.Key, "", map[string]any{
		"Goal": cfg.Goal, "DataDir": cfg.WorkDir, "Now": time.Now().Format("2006-01-02 15:04"),
	})
	if cfg.RequireSelfClaim {
		sysBody += "\n\n【执行器领取协议】先调用 list_intents(states=[\"proposed\"])，核对本 intent id 与 payload.executor 是否都匹配；无匹配时直接结束，不调用其他工具、不启动默认 Worker、不修改状态。找到后调用 claim_executor_intent(intent_id)，确认 state=running 且 owner=" + name + " 后再执行。只有真正完成才允许将 intent 置为 done；无匹配、暂停、报错、停止或耗尽均不得置为 done。"
	} else {
		sysBody += "\n\n【Engine 预领取覆盖说明】本 intent 已由 Engine 以 " + name + " 领取且当前 state=running。忽略 Agent 提示词中任何先 list/claim 的要求，不要调用 list_intents 或 claim_executor_intent，不要重复领取；直接执行下面这条意图。只有真正完成才允许将 intent 置为 done；暂停、报错、停止或耗尽均不得置为 done。"
	}
	sysBody += RenderIntentTask(cfg.Intent) // A1
	sysBody += "\n\n开始执行上面这条意图：只做它、只产生事实、assets、finding、做完即停。"
	system, boundary := deferredSystem(sysBody, def)
	// 公共装配块（intent_exec_base.go）：权限旁路/代理/工作目录/输出溢出目录/待办。
	// 差异字段在本函数覆写：Provider/SystemPrompt 用本 agent 模板、行预算用 agent 行预算、
	// 墙钟预算用该 agent 的 run_seconds（0 = 不限，任务级 deadline 夹逼由 ctx 层负责 taskclock）、
	// 命中预算 → SDK 收尾一轮（与 worker 同款 settlement 语义）。
	opts := baseAgentOptions(runDir, cfg.WorkDir, cfg.ProxyAddr, cfg.ProxyCACert)
	opts.Provider = cfg.Provider
	opts.SystemPrompt = system
	opts.DynamicBoundary = boundary
	opts.Tools = tools
	opts.DeferredTools = def.Deferred
	opts.UnlockSet = def.Unlock
	opts.MaxTurns = cfg.Agent.MaxTurns // 0 = 不限
	opts.MaxDuration = time.Duration(cfg.Agent.RunSecs) * time.Second
	opts.Settlement = wrapupSettlement(cfg.Agent.Key, []string{"Bash"})
	if cfg.Hooks != nil { // typed-nil guard：只挂非空 hook（避免 harness panic）
		opts.Hooks = cfg.Hooks
	}
	// emit 包装：打上执行器名与意图 id，活动流可追溯是哪个执行器跑的。
	emitWrap := wrapIntentEmit(cfg.Emit, name, cfg.Intent.ID)
	input := "执行下面这条意图。"
	if cfg.RequireSelfClaim {
		input = "执行你领到的意图。"
	}
	_, reason, err := captureRun(ctx, opts, input, emitWrap)
	if err != nil {
		// 【二开·P-LOG·A2】异常 WARN（排障：执行器为什么没跑完）。
		log.Printf("[executor] 自定义执行器异常 ⚠ task %d 意图 #%d agent=%s：%v", cfg.TaskID, cfg.Intent.ID, name, err)
		return reason, tsx.Writes(), err
	}
	if cfg.RequireSelfClaim {
		// Agent 必须先通过 claim_executor_intent 领取。未领取且直接退出时保留 proposed/open，
		// 不写占位事实、不落 done，由 Engine 停止 custom 阶段而不是回退默认 Worker。
		current, getErr := cfg.Store.GetNode(cfg.Intent.ID)
		if getErr != nil {
			return reason, tsx.Writes(), getErr
		}
		if current == nil || ((current.State == "proposed" || current.State == "open") && current.Owner != name) {
			log.Printf("[executor] task %d 意图 #%d agent=%s：无匹配领取，直接退出且不启动默认 Worker", cfg.TaskID, cfg.Intent.ID, name)
			return reason, tsx.Writes(), ErrNoMatchingIntent
		}
	}
	// 兜底：什么都没写回时落一条事实，保证图上有执行痕迹（与 worker 兜底同语义）。
	if tsx.Writes().Total() == 0 {
		// 【二开·P-LOG·A2】零写占位 INFO（排障：这条占位 fact 是怎么来的）。
		log.Printf("[executor] 自定义执行器零写入兜底：task %d 意图 #%d agent=%s 补占位 fact", cfg.TaskID, cfg.Intent.ID, name)
		_, _ = cfg.Store.AddNode("fact", map[string]any{
			"text": fmt.Sprintf("意图 #%d 已由执行器 %s 执行（无结构化写回）", cfg.Intent.ID, name),
		}, 0, "confirmed", name, []int64{cfg.Intent.ID})
	}
	// 【二开·P-LOG·A2】完成 INFO（排障：跑没跑完、写了多少、怎么收的官）。
	log.Printf("[executor] 自定义执行器完成：task %d 意图 #%d agent=%s 写入=%d 终止=%s", cfg.TaskID, cfg.Intent.ID, name, tsx.Writes().Total(), reason)
	return reason, tsx.Writes(), nil
}

// 【二开·P-EXEC·A2 自定义执行器】止
