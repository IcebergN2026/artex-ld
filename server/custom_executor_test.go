package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"testing"
	"time"

	"artex/agent"
	"artex/db"
	"norma/harness"
	"norma/llm"
)

// testCustomExecutorSetup 创建独立的 Manager/Server、任务、自定义 agent 和 LLM profile。
// 默认保持 planner_controlled=true 的兼容行为；新契约测试可显式选择 false。
func testCustomExecutorSetup(t *testing.T) (*Manager, *Server, *Task, *db.Agent, int64) {
	return testCustomExecutorSetupWithPlannerControl(t, true)
}

func testCustomExecutorSetupWithPlannerControl(t *testing.T, plannerControlled bool) (*Manager, *Server, *Task, *db.Agent, int64) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	td := t.TempDir()
	serverCtx, serverCancel := context.WithCancel(context.Background())
	s := New(serverCtx, m, td, td, td)

	// 创建测试 LLM profile。
	profileID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name:   fmt.Sprintf("custom-exec-test-%d", time.Now().UnixNano()),
		Format: "openai", Model: "test-model", APIKey: "test-key",
	})
	if err != nil {
		m.Close()
		t.Fatal(err)
	}

	// 创建自定义执行器 agent。
	a, err := m.pg.CreateAgent("recon_scout", "侦察执行器", "测试自定义执行器")
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	if err := m.pg.SetAgentExecutorKey(a.Key, "recon_scout"); err != nil {
		m.Close()
		t.Fatal(err)
	}
	if err := m.pg.SetAgentPlannerControlled(a.Key, plannerControlled); err != nil {
		m.Close()
		t.Fatal(err)
	}
	a.PlannerControlled = plannerControlled
	// 绑定 LLM profile。
	if _, err := m.pg.Exec(`UPDATE agents SET llm_profile_id=$1 WHERE key=$2`, profileID, a.Key); err != nil {
		m.Close()
		t.Fatal(err)
	}
	a.LLMProfileID = &profileID

	// 创建带 pre_agent 的任务。
	task, err := m.CreateTaskWithOptions("测试自定义执行器", "侦察目标", db.TaskCreateOptions{
		PreAgent: "recon_scout",
	})
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	// CreateTaskWithOptions 不自动 seed，显式调用 seedFirstIntent 创建 custom proposed 种子。
	s.seedFirstIntent(task)

	t.Cleanup(func() {
		serverCancel()
		_, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = m.pg.Exec(`DELETE FROM agents WHERE key=$1`, a.Key)
		_, _ = m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, profileID)
		m.Close()
	})
	return m, s, task, a, profileID
}

// seedIntentID 返回任务的种子意图 id（取 kind=intent 中 id 最小的一条）。
func seedIntentID(t *testing.T, task *Task) int64 {
	t.Helper()
	intents, err := task.Store.ListByKind(db.KindIntent, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) == 0 {
		t.Fatal("no seed intent found")
	}
	var minID int64
	for _, n := range intents {
		if minID == 0 || n.ID < minID {
			minID = n.ID
		}
	}
	return minID
}

func waitForNodeState(t *testing.T, store *db.ExplorationStore, id int64, state string, timeout time.Duration) *db.Node {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := store.GetNode(id)
		if err != nil {
			t.Fatal(err)
		}
		if n != nil && n.State == state {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
	n, _ := store.GetNode(id)
	t.Fatalf("intent %d did not reach state %s within %s; current=%+v", id, state, timeout, n)
	return nil
}

// TestCustomExecutorProviderResolution 验证 providerFn 的回退逻辑：
// id=0 或失效绑定回退全局 Provider，有效绑定保持 model，未配置全局返回 ok=false。
func TestCustomExecutorProviderResolution(t *testing.T) {
	_, s, _, _, profileID := testCustomExecutorSetup(t)

	// 有效绑定应返回 provider。
	prov, cfg, ok := s.engine.providerFn(profileID)
	if !ok || prov == nil {
		t.Fatal("valid profile binding should return provider")
	}
	if cfg.Model != "test-model" {
		t.Fatalf("expected model test-model, got %s", cfg.Model)
	}

	// id=0 回退全局（当前未配置全局，应返回 ok=false）。
	_, _, ok = s.engine.providerFn(0)
	if ok {
		t.Fatal("id=0 without global provider should return ok=false")
	}

	// 失效绑定回退全局（同样未配置，ok=false）。
	_, _, ok = s.engine.providerFn(999999)
	if ok {
		t.Fatal("invalid binding without global provider should return ok=false")
	}

	// 配置全局 provider 后，id=0 和失效绑定都应回退成功。
	s.cfgMu.Lock()
	s.llmOn = true
	s.llmProv = &scriptedLLMProvider{}
	s.llmCfg = agent.Config{Model: "global-model"}
	s.cfgMu.Unlock()

	prov, cfg, ok = s.engine.providerFn(0)
	if !ok || prov == nil {
		t.Fatal("id=0 with global provider should return provider")
	}
	if cfg.Model != "global-model" {
		t.Fatalf("expected global-model, got %s", cfg.Model)
	}

	prov, _, ok = s.engine.providerFn(999999)
	if !ok || prov == nil {
		t.Fatal("invalid binding with global provider should fall back")
	}
}

// TestSeedFirstIntentUsesCustomProposed 验证种子意图契约：
// state=proposed、payload.executor=recon_scout、无 review_status、
// Frontier 不领取、HasActiveIntent=true、HasProposedIntent=false、graphOverviewData 含 assigned_intents。
func TestSeedFirstIntentUsesCustomProposed(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetup(t)

	// 重新触发种子（CreateTaskWithOptions 已自动 seed，这里验证已有种子）。
	intents, err := task.Store.ListByKind(db.KindIntent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) == 0 {
		t.Fatal("no seed intent created")
	}
	seed := intents[0]
	if seed.State != "proposed" {
		t.Fatalf("seed state=%s, want proposed", seed.State)
	}
	var p map[string]any
	if err := json.Unmarshal(seed.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["executor"] != "recon_scout" {
		t.Fatalf("seed executor=%v, want recon_scout", p["executor"])
	}
	if _, hasReview := p["review_status"]; hasReview {
		t.Fatal("seed must not have review_status")
	}

	// Frontier 不领取 proposed。
	fr, err := task.Store.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range fr {
		if n.ID == seed.ID {
			t.Fatal("Frontier must not return custom proposed seed")
		}
	}

	// HasActiveIntent=true（custom proposed 算活跃）。
	if active, _ := task.Store.HasActiveIntent(); !active {
		t.Fatal("HasActiveIntent must be true for custom proposed seed")
	}

	// HasProposedIntent=false（不是人工审核）。
	if proposed, _ := task.Store.HasProposedIntent(); proposed {
		t.Fatal("HasProposedIntent must be false for custom proposed seed")
	}

	// AssignedIntents 包含种子（graphOverviewData 的数据源）。
	assigned, err := task.Store.AssignedIntents(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(assigned) == 0 {
		t.Fatal("AssignedIntents must contain custom proposed seed")
	}
	// graphOverviewData 含 assigned_intents（通过 AssignedIntents 非空间接验证）。

	// restoreTaskRuntimes 日志语义（仅验证不 panic）。
	s.restoreTaskRuntimes()
}

// TestCustomExecutorProposedDispatchesWithoutDefaultWorker 验证真实 Engine.Run
// 在默认 worker 不可用时仍能分派 custom proposed 并完成。
func TestCustomExecutorProposedDispatchesWithoutDefaultWorker(t *testing.T) {
	m, s, task, _, _ := testCustomExecutorSetup(t)
	sid := seedIntentID(t, task)

	// 固定 authoritative resolver 返回 nil,nil（默认 worker 不可用）。
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})

	// 注入 scripted provider 到自定义执行器的 providerFn。
	provider := &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{
		{Type: llm.SEMessageStart},
		{Type: llm.SETextDelta, Text: "侦察完成"},
	}}
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return provider, agent.Config{Model: "test-model"}, true
	}

	// 启动真实 Engine.Run。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	// 等待 custom proposed 被领取并完成。
	deadline := time.Now().Add(10 * time.Second)
	var finalState string
	for time.Now().Before(deadline) {
		n, err := task.Store.GetNode(sid)
		if err != nil {
			t.Fatal(err)
		}
		if n == nil {
			t.Fatal("seed intent not found")
		}
		finalState = n.State
		if finalState == "done" || finalState == "blocked" || finalState == "exhausted" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if finalState != "done" {
		t.Fatalf("custom proposed should complete as done, got %s", finalState)
	}

	// Provider 被调用两次：先领取，再执行。
	if provider.calls != 2 {
		t.Fatalf("provider calls=%d, want 2 (claim + execute)", provider.calls)
	}

	// 默认 worker Provider 零调用（通过 resolver 保证）。
	// 活动 Worker 全为 exec:recon_scout。
	acts, _, err := task.Store.ActivityList(nil, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Worker != "" && !strings.HasPrefix(a.Worker, "exec:recon_scout") && a.Worker != "system" && a.Worker != "planner" {
			t.Fatalf("unexpected worker in activity: %s", a.Worker)
		}
	}

	// 普通 open 在同一场景轮询超时仍保持 open 且 owner 为空。
	openID, err := task.Store.AddIntent(map[string]any{"summary": "普通 open 意图"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	on, _ := task.Store.GetNode(openID)
	if on.State != "open" || on.Owner != "" {
		t.Fatalf("default open should remain open with empty owner: state=%s owner=%s", on.State, on.Owner)
	}

	// 另用 nil Provider 证明真实 loop 快速 blocked。
	task2, err := m.CreateTaskWithOptions("nil provider 测试", "目标", db.TaskCreateOptions{PreAgent: "recon_scout"})
	if err != nil {
		t.Fatal(err)
	}
	s.seedFirstIntent(task2)
	defer func() { _, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, task2.ID) }()
	sid2 := seedIntentID(t, task2)
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return nil, agent.Config{}, false
	}
	s.engine.Run(ctx, task2)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := task2.Store.GetNode(sid2)
		if n != nil && n.State == "blocked" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	n2, _ := task2.Store.GetNode(sid2)
	if n2.State != "blocked" {
		t.Fatalf("nil provider should quickly block, got %s", n2.State)
	}
}

// TestCustomExecutorFailureSettlesWorker 验证 nil Provider / panic 时
// 安全边界错误正确清理 work、LLM 计数和 inflight。
func TestCustomExecutorFailureSettlesWorker(t *testing.T) {
	for _, mode := range []string{"nil_provider", "panic_provider"} {
		t.Run(mode, func(t *testing.T) {
			_, s, task, _, _ := testCustomExecutorSetup(t)
			sid := seedIntentID(t, task)
			s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
				return nil, nil
			})

			if mode == "nil_provider" {
				s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
					return nil, agent.Config{}, false
				}
			} else {
				s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
					return &panicLLMProvider{}, agent.Config{Model: "panic"}, true
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.engine.Run(ctx, task)

			// 等待 blocked。
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				n, _ := task.Store.GetNode(sid)
				if n != nil && n.State == "blocked" {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			n, _ := task.Store.GetNode(sid)
			if n.State != "blocked" {
				t.Fatalf("%s should block, got %s", mode, n.State)
			}

			// 恰好一条安全边界 error result。
			acts, _, _ := task.Store.ActivityList(nil, 0, 100)
			var errorResults int
			for _, a := range acts {
				if a.Kind == "result" && a.IsError {
					errorResults++
				}
			}
			if errorResults != 1 {
				t.Fatalf("%s error results=%d, want 1", mode, errorResults)
			}

			// e.work 删除、ActiveLLMCalls/inflight=0。
			if calls := s.engine.ActiveLLMCalls(task.ID); calls != 0 {
				t.Fatalf("%s ActiveLLMCalls=%d, want 0", mode, calls)
			}
			drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
			defer drainCancel()
			if err := s.waitTaskQuiescent(drainCtx, task.ID); err != nil {
				t.Fatalf("%s task did not quiescent within 1s: %v", mode, err)
			}
		})
	}
}

// panicLLMProvider 在 Stream 中 panic，用于测试安全边界。
type panicLLMProvider struct{}

func (p *panicLLMProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		panic("test panic in provider")
	}
}

func (p *panicLLMProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

// TestCustomExecutorModelErrorRetriesSameExecutor 验证 model_error 重试
// 始终使用同一自定义执行器，不转默认 Worker。
func TestCustomExecutorModelErrorRetriesSameExecutor(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetup(t)
	sid := seedIntentID(t, task)
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})

	provider := &scriptedLLMProvider{err: errors.New("transient model error")}
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return provider, agent.Config{Model: "test-model"}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	// 默认重试 2 次（共 3 次调用）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := task.Store.GetNode(sid)
		if n != nil && n.State == "blocked" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	n, _ := task.Store.GetNode(sid)
	if n.State != "blocked" {
		t.Fatalf("model error should end blocked, got %s", n.State)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls=%d, want 3 (1 initial + 2 retries)", provider.calls)
	}

	// 每次重试活动 Worker 均为 exec:recon_scout。
	acts, _, _ := task.Store.ActivityList(nil, 0, 200)
	for _, a := range acts {
		if a.Worker != "" && !strings.HasPrefix(a.Worker, "exec:recon_scout") && a.Worker != "system" && a.Worker != "planner" {
			t.Fatalf("retry used unexpected worker: %s", a.Worker)
		}
	}
}

// TestCustomExecutorPauseResumeAndDelete 验证暂停/恢复/删除生命周期。
func TestCustomExecutorPauseResumeAndDelete(t *testing.T) {
	m, s, task, _, _ := testCustomExecutorSetup(t)
	sid := seedIntentID(t, task)
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})

	// blocking provider：收到 ctx 后返回。
	blocking := &blockingLLMProvider{started: make(chan struct{}), intentID: sid}
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return blocking, agent.Config{Model: "blocking"}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	// 等待 Agent 工具真正完成 proposed → running，而不是仅等待 Provider 发出领取事件。
	<-blocking.started
	n := waitForNodeState(t, task.Store, sid, "running", 3*time.Second)

	// ControlWork(pause) 1 秒内完成并落 paused。
	if err := s.engine.ControlWork(ctx, sid, "pause"); err != nil {
		t.Fatal(err)
	}
	n, _ = task.Store.GetNode(sid)
	if n.State != "paused" {
		t.Fatalf("expected paused, got %s", n.State)
	}

	// resume 后 custom 为 proposed。
	if _, err := s.applyIntentControl(ctx, task, sid, "resume", "", ""); err != nil {
		t.Fatal(err)
	}
	n, _ = task.Store.GetNode(sid)
	if n.State != "proposed" {
		t.Fatalf("resume should return proposed for custom, got %s", n.State)
	}

	// 对未领取 custom proposed 直接 soft cancel。
	task2, err := m.CreateTaskWithOptions("直接删除测试", "目标", db.TaskCreateOptions{PreAgent: "recon_scout"})
	if err != nil {
		t.Fatal(err)
	}
	s.seedFirstIntent(task2)
	defer func() { _, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, task2.ID) }()
	sid2 := seedIntentID(t, task2)
	// 不启动 Engine，直接删除。
	res, err := s.applyIntentControl(ctx, task2, sid2, "cancel", "测试删除", "soft")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != db.StateIntentDeleted {
		t.Fatalf("soft cancel state=%s, want deleted", res.State)
	}
	n2, _ := task2.Store.GetNode(sid2)
	if n2.State != "deleted" {
		t.Fatalf("soft cancel node state=%s", n2.State)
	}

	// 对未领取 custom proposed 直接 hard cancel。
	task3, err := m.CreateTaskWithOptions("彻底删除测试", "目标", db.TaskCreateOptions{PreAgent: "recon_scout"})
	if err != nil {
		t.Fatal(err)
	}
	s.seedFirstIntent(task3)
	defer func() { _, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, task3.ID) }()
	sid3 := seedIntentID(t, task3)
	res, err = s.applyIntentControl(ctx, task3, sid3, "cancel", "测试彻底删除", "hard")
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted == nil {
		t.Fatal("hard cancel should return Deleted")
	}
	if !goneTaskNode(t, task3.Store, sid3) {
		t.Fatal("hard cancel should physically remove node")
	}

	// blocked/exhausted/stopped 可删除，done 仍拒绝。
	for _, state := range []string{"blocked", "exhausted", "stopped"} {
		taskX, err := m.CreateTaskWithOptions(state+"删除测试", "目标", db.TaskCreateOptions{PreAgent: "recon_scout"})
		if err != nil {
			t.Fatal(err)
		}
		s.seedFirstIntent(taskX)
		defer func() { _, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, taskX.ID) }()
		sidX := seedIntentID(t, taskX)
		if _, err := m.pg.Exec(`UPDATE exploration_nodes SET state=$1 WHERE id=$2`, state, sidX); err != nil {
			t.Fatal(err)
		}
		if _, err := s.applyIntentControl(ctx, taskX, sidX, "cancel", "测试", "soft"); err != nil {
			t.Fatalf("%s should be cancellable: %v", state, err)
		}
	}
}

// blockingLLMProvider 在第一次调用时阻塞直到 ctx 取消。
type blockingLLMProvider struct {
	started  chan struct{}
	intentID int64
}

func (p *blockingLLMProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if p.intentID > 0 {
			yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "claim-block", ToolName: "claim_executor_intent"}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: fmt.Sprintf(`{"intent_id":%d}`, p.intentID)}, nil)
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "tool_use"}, nil)
		}
		close(p.started)
		<-ctx.Done()
		yield(llm.StreamEvent{}, ctx.Err())
	}
}

func (p *blockingLLMProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

// goneTaskNode reports whether a node is physically absent.
func goneTaskNode(t *testing.T, store *db.ExplorationStore, id int64) bool {
	t.Helper()
	n, err := store.GetNode(id)
	if err != nil {
		t.Fatal(err)
	}
	return n == nil
}

// TestCustomExecutorTaskPauseReturnsProposed 验证任务级暂停时 custom running 回 proposed。
func TestCustomExecutorTaskPauseReturnsProposed(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetup(t)
	sid := seedIntentID(t, task)
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})
	blocking := &blockingLLMProvider{started: make(chan struct{}), intentID: sid}
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return blocking, agent.Config{Model: "blocking"}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)
	<-blocking.started
	waitForNodeState(t, task.Store, sid, "running", 3*time.Second)

	// 任务级暂停（使用任务级 cause，触发 runIntent 的 taskExecutionPaused 回退分支）。
	s.engine.Pause(task.ID, agent.AbortPausedByUser)
	n := waitForNodeState(t, task.Store, sid, "proposed", 3*time.Second)
	if n.Owner != "" {
		t.Fatalf("task pause should clear owner, got %s", n.Owner)
	}
}

// TestCustomProposedPlannerGates 验证 planner 门控：
// 只有 custom proposed 时 Run 不 kick 首轮、goalless 不落 done、停摆强提示不触发。
func TestCustomProposedPlannerGates(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetup(t)
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})
	sid := seedIntentID(t, task)
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return &scriptedLLMProvider{claimIntentID: sid}, agent.Config{Model: "test"}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	// 给 planner 几轮心跳机会，不应 kick 首轮或重复派单。
	time.Sleep(2 * time.Second)

	intents, _ := task.Store.ListByKind(db.KindIntent, 20)
	if len(intents) != 1 {
		t.Fatalf("planner should not dispatch duplicate intents, got %d", len(intents))
	}

	// review proposed 不被 DispatchFrontier 捞取。
	reviewID, err := task.Store.AddProposedIntent(map[string]any{"summary": "人工审核", "executor": "review"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	dfr, _ := task.Store.DispatchFrontier(20)
	for _, n := range dfr {
		if n.ID == reviewID {
			t.Fatal("review proposed must not appear in DispatchFrontier")
		}
	}
}

// TestCustomExecutorAdmissionWithoutDefaultLLM 验证 ReadyFor=false、CanDispatch=true 时
// custom proposed 不被 LLM 不可用原因排队。
func TestCustomExecutorAdmissionWithoutDefaultLLM(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetup(t)
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})

	if s.engine.ReadyFor(task) {
		t.Fatal("ReadyFor should be false without default worker")
	}
	if !s.engine.CanDispatch(task) {
		t.Fatal("CanDispatch should be true with custom proposed")
	}

	// 启用并发限制后 custom proposed 不被 LLM 不可用原因排队。
	queued, err := s.admitTask(task, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("custom proposed task should not be queued for LLM unavailable")
	}

	// llm_configured=false 语义不变。
	if s.engine.ReadyFor(task) {
		t.Fatal("llm_configured semantics must remain ReadyFor")
	}
}

func TestUncontrolledAgentConfigAffectsAllIntents(t *testing.T) {
	m, s, task, a, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	seedID := seedIntentID(t, task)
	otherID, err := task.Store.AddIntent(map[string]any{
		"summary": "synthetic-non-seed-custom", "executor": "recon_scout",
	}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !s.engine.shouldHoldPlannerForUncontrolledAgent(task) {
		t.Fatal("planner_controlled=false should hold both seed and non-seed custom intents")
	}
	if !s.engine.isUncontrolledByPlanner(&db.Node{Origin: "planner", Payload: json.RawMessage(`{"executor":"recon_scout"}`)}) {
		t.Fatal("notification suppression must use the current Agent config")
	}

	// 同一批既有 intent 随 Agent 当前配置立即改变，不依赖 origin 或 planner_policy。
	if err := m.pg.SetAgentPlannerControlled(a.Key, true); err != nil {
		t.Fatal(err)
	}
	if s.engine.shouldHoldPlannerForUncontrolledAgent(task) {
		t.Fatal("planner_controlled=true should release seed and non-seed custom intents")
	}
	if err := m.pg.SetAgentPlannerControlled(a.Key, false); err != nil {
		t.Fatal(err)
	}
	if !s.engine.shouldHoldPlannerForUncontrolledAgent(task) {
		t.Fatal("planner_controlled=false should hold existing intents again")
	}
	for _, id := range []int64{seedID, otherID} {
		n, err := task.Store.GetNode(id)
		if err != nil || n == nil {
			t.Fatalf("load intent %d: %v", id, err)
		}
		if !s.engine.isUncontrolledByPlanner(n) {
			t.Fatalf("intent %d should be uncontrolled by Planner", id)
		}
	}
}

func TestTerminalStateForIntent(t *testing.T) {
	testErr := errors.New("synthetic-runner-error")
	tests := []struct {
		name   string
		custom bool
		reason harness.TerminalReason
		err    error
		want   string
	}{
		{name: "custom completed", custom: true, reason: harness.ReasonCompleted, want: "done"},
		{name: "custom max turns", custom: true, reason: harness.ReasonMaxTurns, want: "exhausted"},
		{name: "custom timeout", custom: true, reason: harness.ReasonTimeout, want: "exhausted"},
		{name: "custom hook stopped", custom: true, reason: harness.ReasonHookStopped, want: "stopped"},
		{name: "custom stop hook prevented", custom: true, reason: harness.ReasonStopHookPrevented, want: "stopped"},
		{name: "custom aborted streaming", custom: true, reason: harness.ReasonAbortedStreaming, want: "stopped"},
		{name: "custom aborted tools", custom: true, reason: harness.ReasonAbortedTools, want: "stopped"},
		{name: "custom model error without go error", custom: true, reason: harness.ReasonModelError, want: "blocked"},
		{name: "custom unknown reason", custom: true, reason: harness.TerminalReason("synthetic_unknown"), want: "blocked"},
		{name: "custom empty reason", custom: true, reason: harness.TerminalReason(""), want: "blocked"},
		{name: "custom error overrides completed", custom: true, reason: harness.ReasonCompleted, err: testErr, want: "blocked"},
		{name: "ordinary empty reason unchanged", reason: harness.TerminalReason(""), want: "done"},
		{name: "ordinary max turns unchanged", reason: harness.ReasonMaxTurns, want: "exhausted"},
		{name: "ordinary timeout unchanged", reason: harness.ReasonTimeout, want: "exhausted"},
		{name: "ordinary hook stopped unchanged", reason: harness.ReasonHookStopped, want: "done"},
		{name: "ordinary error", reason: harness.ReasonCompleted, err: testErr, want: "blocked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := terminalStateForIntent(tt.custom, tt.reason, tt.err); got != tt.want {
				t.Fatalf("terminalStateForIntent(%v,%q,%v)=%q, want %q", tt.custom, tt.reason, tt.err, got, tt.want)
			}
		})
	}
}

func setPlannerWorkerForTest(t *testing.T, s *Server, plannerProvider, workerProvider *scriptedLLMProvider) {
	t.Helper()
	dir := t.TempDir()
	planner := agent.NewPlanner(plannerProvider, "synthetic-planner", dir, nil, 100000, 2)
	worker := agent.NewWorker(workerProvider, "synthetic-worker", dir, nil, 100000, 2)
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return planner, worker
	})
}

func startShortPlannerLoop(ctx context.Context, s *Server, task *Task) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.engine.plannerLoopWithInterval(ctx, task, 15*time.Millisecond)
	}()
	return done
}

func TestCustomSeedPlannerGateEdgeAndHeartbeat(t *testing.T) {
	states := []string{"proposed", "open", "running", "paused", "stopped", "blocked", "exhausted"}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
			sid := seedIntentID(t, task)
			if err := task.Store.SetIntentState(sid, state); err != nil {
				t.Fatal(err)
			}
			if _, err := task.Store.AddGoal(map[string]any{"text": "synthetic-open-goal"}, "test"); err != nil {
				t.Fatal(err)
			}
			plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
			workerProvider := &scriptedLLMProvider{}
			setPlannerWorkerForTest(t, s, plannerProvider, workerProvider)
			s.engine.debounce = time.Millisecond
			if state == "paused" {
				for range 20 {
					task.enqueueTrigger(agent.TriggerEvent{Kind: "done", IntentID: sid})
				}
				task.enqueueTrigger(agent.TriggerEvent{Kind: "hint", Hints: []string{"synthetic-hint"}})
				task.enqueueTrigger(agent.TriggerEvent{Kind: "revision_requested", IntentID: sid})
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := startShortPlannerLoop(ctx, s, task)
			task.Notify() // 明确覆盖 edge；短 interval 同时覆盖多次 heartbeat。
			time.Sleep(140 * time.Millisecond)
			cancel()
			<-done

			if plannerProvider.calls != 0 {
				t.Fatalf("planner calls=%d, want 0 while seed is %s", plannerProvider.calls, state)
			}
			intents, err := task.Store.ListByKind(db.KindIntent, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(intents) != 1 {
				t.Fatalf("planner produced extra intents while %s: %d", state, len(intents))
			}
			if state == "paused" {
				events := task.drainTriggers()
				if len(events) != 2 {
					t.Fatalf("retained triggers=%d, want hint + revision_requested", len(events))
				}
				kinds := map[string]bool{}
				for _, event := range events {
					kinds[event.Kind] = true
				}
				if !kinds["hint"] || !kinds["revision_requested"] {
					t.Fatalf("retained trigger kinds=%v", kinds)
				}
			}
		})
	}
}

func TestCustomSeedPlannerGateDoneAndControlled(t *testing.T) {
	tests := []struct {
		name       string
		controlled bool
		state      string
	}{
		{name: "done releases planner", controlled: false, state: "done"},
		{name: "planner policy keeps existing behavior", controlled: true, state: "paused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, tt.controlled)
			sid := seedIntentID(t, task)
			if err := task.Store.SetIntentState(sid, tt.state); err != nil {
				t.Fatal(err)
			}
			if _, err := task.Store.AddGoal(map[string]any{"text": "synthetic-open-goal"}, "test"); err != nil {
				t.Fatal(err)
			}
			plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
			setPlannerWorkerForTest(t, s, plannerProvider, &scriptedLLMProvider{})
			s.engine.debounce = time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			done := startShortPlannerLoop(ctx, s, task)
			task.Notify()
			deadline := time.Now().Add(3 * time.Second)
			for plannerProvider.calls == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			<-done
			if plannerProvider.calls == 0 {
				t.Fatal("planner should run when the contract allows it")
			}
		})
	}
}

func TestCustomSeedBlockedDoesNotNotifyPlanner(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	if _, err := task.Store.AddGoal(map[string]any{"text": "synthetic-open-goal"}, "test"); err != nil {
		t.Fatal(err)
	}
	plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
	workerProvider := &scriptedLLMProvider{}
	setPlannerWorkerForTest(t, s, plannerProvider, workerProvider)
	s.engine.providerFn = func(int64) (llm.Provider, agent.Config, bool) {
		return nil, agent.Config{}, false
	}
	s.engine.debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	sid := seedIntentID(t, task)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := task.Store.GetNode(sid)
		if err != nil || n == nil {
			t.Fatalf("load seed: %v", err)
		}
		if n.State == "blocked" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	n, _ := task.Store.GetNode(sid)
	if n.State != "blocked" {
		t.Fatalf("seed state=%s, want blocked", n.State)
	}
	time.Sleep(50 * time.Millisecond)
	if plannerProvider.calls != 0 {
		t.Fatalf("blocked custom seed woke planner %d times", plannerProvider.calls)
	}
	if workerProvider.calls != 0 {
		t.Fatalf("blocked custom seed fell back to default worker %d times", workerProvider.calls)
	}
	for _, event := range task.drainTriggers() {
		if event.Kind == "done" && event.IntentID == sid {
			t.Fatal("blocked custom seed produced a done trigger")
		}
	}
}

func TestCustomSeedDoneAllowsPlanner(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	if _, err := task.Store.AddGoal(map[string]any{"text": "synthetic-open-goal"}, "test"); err != nil {
		t.Fatal(err)
	}
	plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
	setPlannerWorkerForTest(t, s, plannerProvider, &scriptedLLMProvider{})
	sid := seedIntentID(t, task)
	s.engine.providerFn = func(int64) (llm.Provider, agent.Config, bool) {
		return &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-custom-done"}}}, agent.Config{Model: "synthetic-custom"}, true
	}
	s.engine.debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	deadline := time.Now().Add(4 * time.Second)
	for plannerProvider.calls == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if plannerProvider.calls == 0 {
		t.Fatal("completed custom seed should wake planner")
	}
	n, err := task.Store.GetNode(seedIntentID(t, task))
	if err != nil || n == nil {
		t.Fatalf("load completed seed: %v", err)
	}
	if n.State != "done" {
		t.Fatalf("seed state=%s, want done", n.State)
	}
}

func TestCustomSeedGatePreventsGoallessCompletion(t *testing.T) {
	m, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	sid := seedIntentID(t, task)
	if err := task.Store.SetIntentState(sid, "stopped"); err != nil {
		t.Fatal(err)
	}
	plannerProvider := &scriptedLLMProvider{}
	setPlannerWorkerForTest(t, s, plannerProvider, &scriptedLLMProvider{})
	s.engine.debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := startShortPlannerLoop(ctx, s, task)
	task.Notify()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if status := m.TaskStatus(task.ID); status == "done" {
		t.Fatal("unfinished custom seed must not be settled as goalless done")
	}
	if plannerProvider.calls != 0 {
		t.Fatalf("planner calls=%d, want 0", plannerProvider.calls)
	}
}

func TestCustomSeedGateSkipsTimeoutFinalPlanner(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	sid := seedIntentID(t, task)
	if err := task.Store.SetIntentState(sid, "blocked"); err != nil {
		t.Fatal(err)
	}
	plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
	setPlannerWorkerForTest(t, s, plannerProvider, &scriptedLLMProvider{})
	if met := s.engine.runFinalPlannerRound(context.Background(), task); met {
		t.Fatal("unfinished custom seed must not produce a final goal-met judgment")
	}
	if plannerProvider.calls != 0 {
		t.Fatalf("timeout final planner calls=%d, want 0", plannerProvider.calls)
	}
	intents, err := task.Store.ListByKind(db.KindIntent, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 {
		t.Fatalf("timeout final planner produced %d intents, want only the seed", len(intents))
	}
}

// releaseLLMProvider 在 release 前保持 custom 执行中，用于验证默认 Worker 不会提前启动。
type releaseLLMProvider struct {
	started  chan struct{}
	release  chan struct{}
	intentID int64
}

func (p *releaseLLMProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if p.intentID > 0 {
			yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "claim-release", ToolName: "claim_executor_intent"}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: fmt.Sprintf(`{"intent_id":%d}`, p.intentID)}, nil)
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "tool_use"}, nil)
			p.intentID = 0
		}
		select {
		case <-p.started:
		default:
			close(p.started)
		}
		select {
		case <-p.release:
			yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil)
			yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "synthetic-custom-done"}, nil)
		case <-ctx.Done():
			yield(llm.StreamEvent{}, ctx.Err())
		}
	}
}

func (p *releaseLLMProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func TestPreAgentCustomStageIsMutuallyExclusive(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	if _, err := s.m.pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, "recon_scout"); err != nil {
		t.Fatal(err)
	}
	sid := seedIntentID(t, task)
	customProvider := &releaseLLMProvider{started: make(chan struct{}), release: make(chan struct{}), intentID: sid}
	workerProvider := &scriptedLLMProvider{}
	plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
	setPlannerWorkerForTest(t, s, plannerProvider, workerProvider)
	s.engine.providerFn = func(int64) (llm.Provider, agent.Config, bool) {
		return customProvider, agent.Config{Model: "synthetic-custom"}, true
	}
	s.engine.debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)
	<-customProvider.started

	waitForNodeState(t, task.Store, sid, "running", 3*time.Second)
	n, err := task.Store.GetNode(sid)
	if err != nil || n == nil {
		t.Fatalf("load custom seed: %v", err)
	}
	if n.Owner != "exec:recon_scout" {
		t.Fatalf("custom seed owner=%s, want exec:recon_scout", n.Owner)
	}
	defaultID, err := task.Store.AddIntent(map[string]any{"summary": "synthetic-default-followup"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	defaultNode, err := task.Store.GetNode(defaultID)
	if err != nil || defaultNode == nil {
		t.Fatalf("load default intent: %v", err)
	}
	if defaultNode.State != "open" || defaultNode.Owner != "" {
		t.Fatalf("default intent was claimed during custom stage: state=%s owner=%s", defaultNode.State, defaultNode.Owner)
	}
	if workerProvider.calls != 0 {
		t.Fatalf("default worker started during custom stage: calls=%d", workerProvider.calls)
	}

	close(customProvider.release)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		defaultNode, _ = task.Store.GetNode(defaultID)
		if defaultNode != nil && defaultNode.State == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if defaultNode == nil || defaultNode.State != "done" {
		t.Fatalf("default intent was not executed after custom stage: %+v", defaultNode)
	}
	if workerProvider.calls == 0 {
		t.Fatal("default worker did not start after custom stage")
	}

	// planner + custom wrapper + 延迟 Worker 池必须全部 Done，StopTask 不得永久等待。
	stopped := make(chan struct{})
	go func() {
		s.engine.StopTask(task.ID)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("StopTask hung: goroutine count mismatch after delayed worker startup")
	}
}

func TestPreAgentTaskCreateTriggerUsesConversationOnly(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: "recon_scout", Enabled: true, OnTaskCreate: true,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, "recon_scout")
	})
	sid := seedIntentID(t, task)
	customProvider := &scriptedLLMProvider{}
	workerProvider := &scriptedLLMProvider{}
	plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
	setPlannerWorkerForTest(t, s, plannerProvider, workerProvider)
	s.engine.providerFn = func(int64) (llm.Provider, agent.Config, bool) {
		return customProvider, agent.Config{Model: "synthetic-custom"}, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)
	time.Sleep(150 * time.Millisecond)

	seed, err := task.Store.GetNode(sid)
	if err != nil || seed == nil {
		t.Fatalf("load seed: %v", err)
	}
	if seed.State != "proposed" || seed.Owner != "" {
		t.Fatalf("triggered conversation duplicated by custom executor: state=%s owner=%s", seed.State, seed.Owner)
	}
	if customProvider.calls != 0 {
		t.Fatalf("customExecutorLoop started despite on_task_create: calls=%d", customProvider.calls)
	}
	defaultID, err := task.Store.AddIntent(map[string]any{"summary": "conversation-followup"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := task.Store.ClaimExecutorIntent(sid, "proposed", "recon_scout", "exec:recon_scout")
	if err != nil || !claimed {
		t.Fatalf("simulate conversation claim: claimed=%v err=%v", claimed, err)
	}
	time.Sleep(150 * time.Millisecond)
	followup, err := task.Store.GetNode(defaultID)
	if err != nil || followup == nil {
		t.Fatalf("load followup: %v", err)
	}
	if followup.State != "open" || followup.Owner != "" {
		t.Fatalf("default worker claimed during conversation: state=%s owner=%s", followup.State, followup.Owner)
	}
	if workerProvider.calls != 0 {
		t.Fatalf("default worker started during conversation: calls=%d", workerProvider.calls)
	}
	if err := task.Store.SetNodeState(sid, "done"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		followup, _ = task.Store.GetNode(defaultID)
		if followup != nil && followup.State == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if followup == nil || followup.State != "done" {
		t.Fatalf("default worker did not resume after conversation: %+v", followup)
	}
	s.engine.StopTask(task.ID)
}

func TestConversationExecutorToolInjection(t *testing.T) {
	m, _, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	wireTools(m.pg, buildDomainReg(m.Assets()))
	t.Cleanup(func() { agent.ToolResolve = nil })
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: taskID})
	resolved := names(agent.ToolResolve(ctx, "recon_scout", nil))
	if resolved["list_intents"] == nil || resolved["claim_executor_intent"] == nil {
		t.Fatalf("conversation tools not injected: list=%v claim=%v", resolved["list_intents"] != nil, resolved["claim_executor_intent"] != nil)
	}
	sid := seedIntentID(t, task)
	res, err := resolved["claim_executor_intent"].Call(ctx, json.RawMessage(fmt.Sprintf(`{"intent_id":%d}`, sid)), nil)
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), `"claimed":true`) {
		t.Fatalf("conversation claim failed: err=%v result=%s", err, res.Flatten())
	}
	n, err := task.Store.GetNode(sid)
	if err != nil || n == nil {
		t.Fatalf("load claimed seed: %v", err)
	}
	if n.State != "running" || n.Owner != "exec:recon_scout" {
		t.Fatalf("claimed state=%s owner=%s", n.State, n.Owner)
	}
	internalTools := names(agent.ToolResolve(ctx, "exec:recon_scout", nil))
	if internalTools["list_intents"] == nil || internalTools["claim_executor_intent"] == nil {
		t.Fatal("exec:<key> identity lost executor tools after catalog binding")
	}
	workerTools := names(agent.ToolResolve(ctx, "worker", nil))
	if workerTools["claim_executor_intent"] != nil {
		t.Fatal("default worker must not receive claim_executor_intent through injection")
	}
}

func TestCustomExecutorNoMatchExitsWithoutDone(t *testing.T) {
	_, _, task, a, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	intent, err := task.Store.GetNode(seedIntentID(t, task))
	if err != nil || intent == nil {
		t.Fatalf("load proposed intent: %v", err)
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	reason, _, err := agent.RunCustomExecutor(context.Background(), agent.CustomExecutorConfig{
		Agent: a, Provider: &scriptedLLMProvider{}, TaskID: taskID,
		Intent: intent, Store: task.Store, Goal: "synthetic-goal", WorkDir: t.TempDir(),
	})
	if !errors.Is(err, agent.ErrNoMatchingIntent) {
		t.Fatalf("expected ErrNoMatchingIntent, got reason=%s err=%v", reason, err)
	}
	n, err := task.Store.GetNode(intent.ID)
	if err != nil || n == nil {
		t.Fatalf("reload intent: %v", err)
	}
	if n.State != "proposed" || n.Owner != "" {
		t.Fatalf("no-match intent changed state=%s owner=%s, want proposed/empty", n.State, n.Owner)
	}
}

func TestCustomExecutorSelfClaimCompletesDone(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	sid := seedIntentID(t, task)
	provider := &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{
		{Type: llm.SEMessageStart},
		{Type: llm.SETextDelta, Text: "synthetic-self-claim-done"},
	}}
	s.engine.providerFn = func(int64) (llm.Provider, agent.Config, bool) {
		return provider, agent.Config{Model: "synthetic-custom"}, true
	}
	intent, err := task.Store.GetNode(sid)
	if err != nil || intent == nil {
		t.Fatalf("load proposed intent: %v", err)
	}
	if claimed := s.engine.runIntent(context.Background(), task, "exec:recon_scout", nil, intent, "", ""); !claimed {
		t.Fatal("custom executor should claim and complete the proposed intent")
	}
	n, err := task.Store.GetNode(sid)
	if err != nil || n == nil {
		t.Fatalf("reload completed intent: %v", err)
	}
	if n.State != "done" || n.Owner != "exec:recon_scout" {
		t.Fatalf("self-claimed intent state=%s owner=%s, want done/exec:recon_scout", n.State, n.Owner)
	}
}
