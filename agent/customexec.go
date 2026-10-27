// ═══ ARTEX 二开 · [P-EXEC·A2 自定义执行器] ═══
// 功能介绍：RunCustomExecutor——意图级自定义执行器执行链（K18/K23）；骨架复刻 worker execute() 装配（agent/worker.go:365-470），
//
//	差异只在：①系统提示词=自定义 agent 的模板（renderSystem 按 agent.Key）②工具=WorkerTools 底座+agent augment ③意图块=A1 导出的 RenderIntentTask
//
// 二开日期：2026-10-21
package agent

import (
	"context"
	"fmt"
	"log" // 【二开·P-LOG·A2】INFO/WARN 级别直接用 log
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	agentcore "github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
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
}

// RunCustomExecutor 用声明 executor_key 的自定义 agent 执行一条意图。
// 与 worker.execute() 的差异只有三处（其余逐行对齐）：
//  1. 系统提示词：renderSystem(cfg.Agent.Key, "", vars)——模板从该 agent 的提示词覆盖链取；
//  2. 意图任务块：RenderIntentTask（A1 导出，与 worker 同文案）；
//  3. ToolSet.worker 名 = "exec:"+cfg.Agent.Key（活动流可追溯是哪个执行器跑的）。
//
// 返回 (终态原因, 写入统计, 错误)，与 worker.Execute 同签名语义，runIntent 终态分流零改动。
func RunCustomExecutor(ctx context.Context, cfg CustomExecutorConfig) (harness.TerminalReason, WriteCounts, error) {
	name := "exec:" + cfg.Agent.Key
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
	// 工具底座与 worker 同款：WorkerTools + 默认工具族（去 MultiEdit/Glob/Grep）+ agent augment。
	base := append(tsx.WorkerTools(), defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: cfg.TaskID, ExplorationID: explorationID(cfg.Store), IntentID: cfg.Intent.ID})
	tools, def, cleanup := AugmentTools(ctx, cfg.Agent.Key, base)
	defer cleanup()
	// 本意图的专属工作目录 <workDir>/tasks/<taskID>/i<intentID>（与 worker 同款）。
	runDir := ensureRunDir(cfg.WorkDir, cfg.TaskID, cfg.Intent.ID)
	// 动作审核上下文：工作目录即会话归属（与 worker 同机制；不继承父 run 的 background）。
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	// 【二开·P-LOG·A2】启动日志：INFO 记开始执行（排障：这个意图是谁在跑）。
	log.Printf("[executor] 自定义执行器启动：task %d 意图 #%d agent=%s（%s）", cfg.TaskID, cfg.Intent.ID, cfg.Agent.Name, cfg.Agent.Key)
	// 系统提示词：该自定义 agent 的模板（renderSystem 走提示词覆盖链）+ 意图块（A1）+ 启动指令。
	sysBody := renderSystem(cfg.Agent.Key, "", map[string]any{
		"Goal": cfg.Goal, "DataDir": cfg.WorkDir, "Now": time.Now().Format("2006-01-02 15:04"),
	})
	sysBody += RenderIntentTask(cfg.Intent) // A1
	sysBody += "\n\n开始执行上面这条意图：只做它、只产生事实、assets、finding、做完即停。"
	system, boundary := deferredSystem(sysBody, def)
	opts := agentcore.Options{
		Provider:        cfg.Provider,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch/Bash 走记录代理（与 worker 同款留痕语义）。
		EnableWebFetch: true,
		WebFetchProxy:  cfg.ProxyAddr,
		WebFetchCACert: cfg.ProxyCACert,
		BashEnv:        proxyEnv(cfg.ProxyAddr, cfg.ProxyCACert),
		WorkingDir:     runDir,
		Root:           cfg.WorkDir, // 文件工具硬边界（同 worker）
		MaxTurns:       cfg.Agent.MaxTurns, // 0 = 不限
		// 墙钟预算：用该 agent 的 run_seconds（0 = 不限）。任务级 deadline 夹逼由 ctx 层负责（taskclock）。
		MaxDuration: time.Duration(cfg.Agent.RunSecs) * time.Second,
		// 命中预算 → SDK 收尾一轮（与 worker 同款 settlement 语义）。
		Settlement:    wrapupSettlement(cfg.Agent.Key, []string{"Bash"}),
		ToolOutputDir: cmdOutDir(runDir),
		Todos:         actool.NewTodoStore(),
	}
	if cfg.Hooks != nil { // typed-nil guard：只挂非空 hook（避免 harness panic）
		opts.Hooks = cfg.Hooks
	}
	// emit 包装：打上执行器名与意图 id，活动流可追溯是哪个执行器跑的。
	intentID := cfg.Intent.ID
	emitWrap := func(a db.Activity) {
		if cfg.Emit != nil {
			a.NodeID, a.Worker = &intentID, name
			cfg.Emit(a)
		}
	}
	input := "执行你领到的意图。"
	_, reason, err := captureRun(ctx, opts, input, emitWrap)
	if err != nil {
		// 【二开·P-LOG·A2】异常 WARN（排障：执行器为什么没跑完）。
		log.Printf("[executor] 自定义执行器异常 ⚠ task %d 意图 #%d agent=%s：%v", cfg.TaskID, cfg.Intent.ID, cfg.Agent.Key, err)
		return reason, tsx.Writes(), err
	}
	// 兜底：什么都没写回时落一条事实，保证图上有执行痕迹（与 worker 兜底同语义）。
	if tsx.Writes().Total() == 0 {
		// 【二开·P-LOG·A2】零写占位 INFO（排障：这条占位 fact 是怎么来的）。
		log.Printf("[executor] 自定义执行器零写入兜底：task %d 意图 #%d agent=%s 补占位 fact", cfg.TaskID, cfg.Intent.ID, cfg.Agent.Key)
		_, _ = cfg.Store.AddNode("fact", map[string]any{
			"text": fmt.Sprintf("意图 #%d 已由执行器 %s 执行（无结构化写回）", cfg.Intent.ID, cfg.Agent.Key),
		}, 0, "confirmed", name, []int64{cfg.Intent.ID})
	}
	// 【二开·P-LOG·A2】完成 INFO（排障：跑没跑完、写了多少、怎么收的官）。
	log.Printf("[executor] 自定义执行器完成：task %d 意图 #%d agent=%s 写入=%d 终止=%s", cfg.TaskID, cfg.Intent.ID, cfg.Agent.Key, tsx.Writes().Total(), reason)
	return reason, tsx.Writes(), nil
}

// 【二开·P-EXEC·A2】止
