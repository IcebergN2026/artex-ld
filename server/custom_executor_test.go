package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
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

	// Agent.Key 与 ExecutorKey 故意不同：会话/提示词使用前者，领取身份使用后者。
	a, err := m.pg.CreateAgent("recon_scout_agent", "侦察执行器", "测试自定义执行器")
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	if err := m.pg.SetAgentExecutorKey(a.Key, "recon_scout"); err != nil {
		m.Close()
		t.Fatal(err)
	}
	a.ExecutorKey = "recon_scout"
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

// installProfileChatAgentForTest 让自定义 Agent 的绑定 profile 走真实 ChatAgent，
// 仅将 LLM 传输层替换为脚本 provider；Conversation、工具装配与结算均走生产实现。
func installProfileChatAgentForTest(s *Server, profileID int64, provider llm.Provider) {
	ca := agent.NewChatAgent(provider, "test-model", s.m.dir, nil, 100000)
	s.profMu.Lock()
	s.profChatAgents[profileID] = ca
	s.profMu.Unlock()
}

func wireConversationToolsForTest(t *testing.T, m *Manager) {
	t.Helper()
	wireTools(m.pg, buildDomainReg(m.Assets()))
	t.Cleanup(func() { agent.ToolResolve = nil })
}

// waitForPreAgentConversation 等到真实 Conversation 的终态活动落库，不用固定睡眠猜测执行完成。
func waitForPreAgentConversation(t *testing.T, s *Server, task *Task, agentKey string) (*db.Conversation, []db.Activity) {
	t.Helper()
	title := fmt.Sprintf("首个意图 · task#%s", task.ID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conversations, err := s.m.pg.ListConversations()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range conversations {
			if c.AgentKey != agentKey || c.Title != title {
				continue
			}
			activities, _, err := s.m.pg.ConvActivityList(c.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			for _, activity := range activities {
				if activity.Kind == "result" {
					return c, activities
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pre_agent conversation %q for agent %s did not finish", title, agentKey)
	return nil, nil
}

func preAgentConversationUserMessage(t *testing.T, s *Server, conversationID int64) string {
	t.Helper()
	activities, _, err := s.m.pg.ConvActivityList(conversationID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, activity := range activities {
		if activity.Kind != "user" {
			continue
		}
		detail, err := s.m.pg.ConvActivityDetail(conversationID, activity.ID)
		if err != nil {
			t.Fatal(err)
		}
		return detail
	}
	t.Fatalf("conversation %d has no user activity", conversationID)
	return ""
}

func assertNoTaskActivityForIntent(t *testing.T, task *Task, intentID int64) {
	t.Helper()
	activities, _, err := task.Store.ActivityList(nil, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, activity := range activities {
		if activity.NodeID != nil && *activity.NodeID == intentID {
			t.Fatalf("conversation-only seed emitted task activity: %+v", activity)
		}
	}
}

func stopTaskWithin(t *testing.T, s *Server, taskID string) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		s.engine.StopTask(taskID)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("StopTask did not return within 2s")
	}
}

func addNonSeedCustomIntent(t *testing.T, task *Task, state string) *db.Node {
	t.Helper()
	id, err := task.Store.AddNode(db.KindIntent, map[string]any{
		"summary": "synthetic-non-seed-custom", "executor": "recon_scout",
	}, 5, state, "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.GetNode(id)
	if err != nil || intent == nil {
		t.Fatalf("load non-seed custom intent: %+v err=%v", intent, err)
	}
	return intent
}

func claimNonSeedCustomIntent(t *testing.T, task *Task) *db.Node {
	t.Helper()
	intent := addNonSeedCustomIntent(t, task, "open")
	claimed, err := task.Store.ClaimExecutorIntent(intent.ID, "open", "recon_scout", "exec:recon_scout")
	if err != nil || !claimed {
		t.Fatalf("preclaim non-seed custom intent: claimed=%v err=%v", claimed, err)
	}
	intent, err = task.Store.GetNode(intent.ID)
	if err != nil || intent == nil {
		t.Fatalf("reload preclaimed intent: %+v err=%v", intent, err)
	}
	return intent
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
	_, _, task, _, _ := testCustomExecutorSetup(t)

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
}

// TestPreAgentConversationClaimsAndCompletesSeed 验证首个种子只经真实
// Conversation/ChatAgent 执行，并严格区分 Agent.Key 与 ExecutorKey。
func TestPreAgentConversationClaimsAndCompletesSeed(t *testing.T) {
	_, s, task, a, profileID := testCustomExecutorSetupWithPlannerControl(t, false)
	if a.Key == a.ExecutorKey {
		t.Fatal("test requires distinct Agent.Key and ExecutorKey")
	}
	sid := seedIntentID(t, task)
	provider := &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{
		{Type: llm.SEMessageStart},
		{Type: llm.SETextDelta, Text: "侦察完成"},
	}}
	installProfileChatAgentForTest(s, profileID, provider)
	wireConversationToolsForTest(t, s.m)
	workerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "default worker must stay idle"},
	}}
	setPlannerWorkerForTest(t, s, &scriptedLLMProvider{}, workerProvider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	seed := waitForNodeState(t, task.Store, sid, "done", 5*time.Second)
	if seed.Owner != "exec:"+a.ExecutorKey {
		t.Fatalf("seed owner=%q, want exec:%s", seed.Owner, a.ExecutorKey)
	}
	conversation, activities := waitForPreAgentConversation(t, s, task, a.Key)
	if conversation.AgentKey != a.Key {
		t.Fatalf("conversation agent=%q, want Agent.Key %q", conversation.AgentKey, a.Key)
	}
	seenClaim := false
	for _, activity := range activities {
		if activity.Kind == "tool_use" && activity.Tool == "claim_executor_intent" {
			seenClaim = true
		}
		if activity.Worker != "" && activity.Worker != a.Key {
			t.Fatalf("conversation activity worker=%q, want %q", activity.Worker, a.Key)
		}
	}
	if !seenClaim {
		t.Fatal("real Conversation did not execute claim_executor_intent")
	}
	if provider.calls != 2 {
		t.Fatalf("ChatAgent provider calls=%d, want 2 (claim + final answer)", provider.calls)
	}
	if workerProvider.calls != 0 {
		t.Fatalf("default Worker executed the conversation-only seed: calls=%d", workerProvider.calls)
	}
	assertNoTaskActivityForIntent(t, task, sid)
	stopTaskWithin(t, s, task.ID)
}

// TestCustomExecutorFailureSettlesWorker 验证非种子 custom intent 在 nil Provider / panic 时
// 仍由 runIntent 安全边界清理 work、LLM 计数和 inflight。
func TestCustomExecutorFailureSettlesWorker(t *testing.T) {
	for _, mode := range []string{"nil_provider", "panic_provider"} {
		t.Run(mode, func(t *testing.T) {
			_, s, task, _, _ := testCustomExecutorSetup(t)
			intent := claimNonSeedCustomIntent(t, task)

			if mode == "nil_provider" {
				s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
					return nil, agent.Config{}, false
				}
			} else {
				s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
					return &panicLLMProvider{}, agent.Config{Model: "panic"}, true
				}
			}

			if settled := s.engine.runIntent(context.Background(), task, "exec:recon_scout", nil, intent, "", "", true); !settled {
				t.Fatal("preclaimed custom intent must be settled by runIntent")
			}
			n, err := task.Store.GetNode(intent.ID)
			if err != nil || n == nil {
				t.Fatalf("reload intent: %+v err=%v", n, err)
			}
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

// TestCustomExecutorModelErrorRetriesSameExecutor 验证非种子 custom intent 的
// model_error 重试始终使用同一自定义执行器，不转默认 Worker。
func TestCustomExecutorModelErrorRetriesSameExecutor(t *testing.T) {
	m, s, task, _, _ := testCustomExecutorSetup(t)
	originalPolicy := m.pg.LLMRetryPolicy()
	policy := originalPolicy
	policy.Intent = db.RetryRule{Attempts: 2, IntervalMS: 1}
	if err := m.pg.SetLLMRetryPolicy(policy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.SetLLMRetryPolicy(originalPolicy) })
	intent := claimNonSeedCustomIntent(t, task)
	provider := &scriptedLLMProvider{err: errors.New("transient model error")}
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return provider, agent.Config{Model: "test-model"}, true
	}

	if settled := s.engine.runIntent(context.Background(), task, "exec:recon_scout", nil, intent, "", "", true); !settled {
		t.Fatal("preclaimed custom intent must settle after retries")
	}
	n, err := task.Store.GetNode(intent.ID)
	if err != nil || n == nil {
		t.Fatalf("reload intent: %+v err=%v", n, err)
	}
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
	intent := claimNonSeedCustomIntent(t, task)

	// 非种子 intent 已由 Engine 预领取；blocking provider 只负责保持 runIntent 在执行中。
	blocking := &blockingLLMProvider{started: make(chan struct{})}
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return blocking, agent.Config{Model: "blocking"}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan bool, 1)
	go func() {
		runDone <- s.engine.runIntent(ctx, task, "exec:recon_scout", nil, intent, "", "", true)
	}()
	<-blocking.started

	// ControlWork(pause) 完成后落 paused。
	if err := s.engine.ControlWork(ctx, intent.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if settled := <-runDone; !settled {
		t.Fatal("paused custom intent must remain a settled runIntent call")
	}
	n, err := task.Store.GetNode(intent.ID)
	if err != nil || n == nil {
		t.Fatalf("reload paused intent: %+v err=%v", n, err)
	}
	if n.State != "paused" {
		t.Fatalf("expected paused, got %s", n.State)
	}

	// resume 后 custom 为 proposed。
	if _, err := s.applyIntentControl(ctx, task, intent.ID, "resume", "", ""); err != nil {
		t.Fatal(err)
	}
	n, _ = task.Store.GetNode(intent.ID)
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

// blockingLLMProvider 保持 preclaimed custom 执行中，直到 ctx 取消。
type blockingLLMProvider struct {
	started chan struct{}
}

func (p *blockingLLMProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
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

// TestCustomExecutorTaskPauseReturnsProposed 验证任务级暂停时非种子 custom running 回 proposed。
func TestCustomExecutorTaskPauseReturnsProposed(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetup(t)
	intent := claimNonSeedCustomIntent(t, task)
	blocking := &blockingLLMProvider{started: make(chan struct{})}
	s.engine.providerFn = func(profileID int64) (llm.Provider, agent.Config, bool) {
		return blocking, agent.Config{Model: "blocking"}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan bool, 1)
	go func() {
		runDone <- s.engine.runIntent(ctx, task, "exec:recon_scout", nil, intent, "", "", true)
	}()
	<-blocking.started

	s.engine.Pause(task.ID, agent.AbortPausedByUser)
	n := waitForNodeState(t, task.Store, intent.ID, "proposed", 3*time.Second)
	if n.Owner != "" {
		t.Fatalf("task pause should clear owner, got %s", n.Owner)
	}
	if settled := <-runDone; !settled {
		t.Fatal("task-paused custom intent must remain a settled runIntent call")
	}
}

// TestCustomProposedPlannerGates 验证 planner 门控：真实 pre_agent 对话无匹配退出后，
// 只有 custom proposed 时仍不 kick 首轮、不重复派单。
func TestCustomProposedPlannerGates(t *testing.T) {
	_, s, task, a, profileID := testCustomExecutorSetup(t)
	provider := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "no matching intent"},
	}}
	installProfileChatAgentForTest(s, profileID, provider)
	wireConversationToolsForTest(t, s.m)
	plannerProvider := &scriptedLLMProvider{}
	setPlannerWorkerForTest(t, s, plannerProvider, &scriptedLLMProvider{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)
	waitForPreAgentConversation(t, s, task, a.Key)

	intents, err := task.Store.ListByKind(db.KindIntent, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 {
		t.Fatalf("planner should not dispatch duplicate intents, got %d", len(intents))
	}
	if plannerProvider.calls != 0 {
		t.Fatalf("planner calls=%d, want 0 while custom seed remains proposed", plannerProvider.calls)
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
	stopTaskWithin(t, s, task.ID)
	if plannerProvider.calls != 0 {
		t.Fatalf("planner ran during shutdown gate check: calls=%d", plannerProvider.calls)
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
	// admitTask starts the runtime asynchronously. Stop it and wait for its
	// conversation/routines to settle before t.TempDir cleanup runs.
	stopTaskWithin(t, s, task.ID)

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

func TestCustomSeedDoneAllowsPlanner(t *testing.T) {
	_, s, task, a, profileID := testCustomExecutorSetupWithPlannerControl(t, false)
	if _, err := task.Store.AddGoal(map[string]any{"text": "synthetic-open-goal"}, "test"); err != nil {
		t.Fatal(err)
	}
	plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
	setPlannerWorkerForTest(t, s, plannerProvider, &scriptedLLMProvider{})
	sid := seedIntentID(t, task)
	provider := &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-custom-done"},
	}}
	installProfileChatAgentForTest(s, profileID, provider)
	wireConversationToolsForTest(t, s.m)
	s.engine.debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	waitForNodeState(t, task.Store, sid, "done", 5*time.Second)
	deadline := time.Now().Add(4 * time.Second)
	for plannerProvider.calls == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if plannerProvider.calls == 0 {
		t.Fatal("completed custom seed should wake planner")
	}
	waitForPreAgentConversation(t, s, task, a.Key)
	stopTaskWithin(t, s, task.ID)
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

// releaseLLMProvider 第一轮真实领取种子，第二轮在 release 前保持 Conversation 执行中。
type releaseLLMProvider struct {
	started  chan struct{}
	release  chan struct{}
	intentID int64
	calls    int
}

func (p *releaseLLMProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls++
		if p.calls == 1 {
			yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "claim-release", ToolName: "claim_executor_intent"}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: fmt.Sprintf(`{"intent_id":%d}`, p.intentID)}, nil)
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "tool_use"}, nil)
			return
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
	_, s, task, a, profileID := testCustomExecutorSetupWithPlannerControl(t, false)
	sid := seedIntentID(t, task)
	customProvider := &releaseLLMProvider{started: make(chan struct{}), release: make(chan struct{}), intentID: sid}
	installProfileChatAgentForTest(s, profileID, customProvider)
	wireConversationToolsForTest(t, s.m)
	workerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-default-done"},
	}}
	plannerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-noop"}}}
	setPlannerWorkerForTest(t, s, plannerProvider, workerProvider)
	s.engine.debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)

	n := waitForNodeState(t, task.Store, sid, "running", 3*time.Second)
	<-customProvider.started // 第二轮已到来：claim 工具已真实执行，Conversation 仍在运行。
	if n.Owner != "exec:recon_scout" {
		t.Fatalf("custom seed owner=%s, want exec:recon_scout", n.Owner)
	}
	defaultID, err := task.Store.AddIntent(map[string]any{"summary": "synthetic-default-followup"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	defaultNode, err := task.Store.GetNode(defaultID)
	if err != nil || defaultNode == nil {
		t.Fatalf("load default intent: %v", err)
	}
	if defaultNode.State != "open" || defaultNode.Owner != "" {
		t.Fatalf("default intent was claimed during conversation: state=%s owner=%s", defaultNode.State, defaultNode.Owner)
	}
	if workerProvider.calls != 0 {
		t.Fatalf("default worker started during conversation: calls=%d", workerProvider.calls)
	}

	close(customProvider.release)
	waitForNodeState(t, task.Store, sid, "done", 5*time.Second)
	waitForNodeState(t, task.Store, defaultID, "done", 5*time.Second)
	if workerProvider.calls == 0 {
		t.Fatal("default worker did not start after conversation completed")
	}
	waitForPreAgentConversation(t, s, task, a.Key)
	// planner + Conversation wrapper + 延迟 Worker 池必须全部 Done。
	stopTaskWithin(t, s, task.ID)
}

func TestPreAgentTaskCreateTriggerUsesConversationOnly(t *testing.T) {
	_, s, task, a, profileID := testCustomExecutorSetupWithPlannerControl(t, false)
	const marker = "synthetic-task-create-marker"
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: a.Key, Enabled: true, OnTaskCreate: true, TaskCreateMessage: marker,
	}); err != nil {
		t.Fatal(err)
	}
	sid := seedIntentID(t, task)
	provider := &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "synthetic-custom-done"},
	}}
	installProfileChatAgentForTest(s, profileID, provider)
	wireConversationToolsForTest(t, s.m)
	workerProvider := &scriptedLLMProvider{}
	setPlannerWorkerForTest(t, s, &scriptedLLMProvider{}, workerProvider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)
	waitForNodeState(t, task.Store, sid, "done", 5*time.Second)
	conversation, _ := waitForPreAgentConversation(t, s, task, a.Key)
	message := preAgentConversationUserMessage(t, s, conversation.ID)
	if !strings.Contains(message, marker+"\n\n【本次由任务创建触发】") {
		t.Fatalf("on_task_create message was not merged into pre_agent Conversation: %q", message)
	}
	if !strings.Contains(message, "【首个意图执行协议】") {
		t.Fatalf("pre_agent protocol missing from Conversation input: %q", message)
	}
	if workerProvider.calls != 0 {
		t.Fatalf("on_task_create fell back to default Worker: calls=%d", workerProvider.calls)
	}
	assertNoTaskActivityForIntent(t, task, sid)
	stopTaskWithin(t, s, task.ID)
}

func TestPreAgentConversationNoMatchLeavesSeedAndWorkerIdle(t *testing.T) {
	_, s, task, a, profileID := testCustomExecutorSetupWithPlannerControl(t, false)
	sid := seedIntentID(t, task)
	provider := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "no matching seed"},
	}}
	installProfileChatAgentForTest(s, profileID, provider)
	wireConversationToolsForTest(t, s.m)
	workerProvider := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "must not run"},
	}}
	setPlannerWorkerForTest(t, s, &scriptedLLMProvider{}, workerProvider)
	defaultID, err := task.Store.AddIntent(map[string]any{"summary": "must-not-fall-back"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.engine.Run(ctx, task)
	conversation, _ := waitForPreAgentConversation(t, s, task, a.Key)
	if conversation.ID == 0 {
		t.Fatal("no-match path must still use a real Conversation")
	}
	stopTaskWithin(t, s, task.ID)

	seed, err := task.Store.GetNode(sid)
	if err != nil || seed == nil {
		t.Fatalf("reload seed: %+v err=%v", seed, err)
	}
	if seed.State != "proposed" || seed.Owner != "" {
		t.Fatalf("no-match seed state=%s owner=%q, want proposed/empty", seed.State, seed.Owner)
	}
	facts, err := task.Store.FactsYielded(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Fatalf("no-match wrote placeholder task facts: %v", facts)
	}
	defaultNode, err := task.Store.GetNode(defaultID)
	if err != nil || defaultNode == nil {
		t.Fatalf("reload default intent: %+v err=%v", defaultNode, err)
	}
	if defaultNode.State != "open" || defaultNode.Owner != "" {
		t.Fatalf("no-match fell back to default Worker: state=%s owner=%q", defaultNode.State, defaultNode.Owner)
	}
	if workerProvider.calls != 0 {
		t.Fatalf("default Worker calls=%d, want 0", workerProvider.calls)
	}
	if provider.calls != 1 {
		t.Fatalf("no-match ChatAgent provider calls=%d, want 1", provider.calls)
	}
	assertNoTaskActivityForIntent(t, task, sid)
}

func TestPreAgentTransientErrorRetries(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	calls := 0
	s.engine.preAgentFn = func(context.Context, *Task) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("transient database error")
		}
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.engine.runPreAgentStage(ctx, task) {
		t.Fatal("no-match run must not release workers")
	}
	if calls != 2 {
		t.Fatalf("pre-agent calls=%d, want retry after transient error", calls)
	}
}

func TestHasOnTaskCreateTriggerUsesAgentKey(t *testing.T) {
	_, s, _, a, _ := testCustomExecutorSetup(t)
	if a.Key == a.ExecutorKey {
		t.Fatal("test requires distinct Agent.Key and ExecutorKey")
	}
	// The package shares one PostgreSQL database across tests; remove triggers left
	// by earlier cases so this test starts from a known empty trigger set.
	if err := s.m.pg.DeleteTriggersForAgent(a.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: a.Key, Enabled: false, OnTaskCreate: true, TaskCreateMessage: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: a.Key, Enabled: true, OnFinding: true, FindingMessage: "other event",
	}); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.engine.hasOnTaskCreateTrigger(a.ExecutorKey)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("disabled or non-create trigger must not enable on_task_create")
	}
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: a.Key, Enabled: true, OnTaskCreate: true, TaskCreateMessage: "enabled",
	}); err != nil {
		t.Fatal(err)
	}
	enabled, err = s.engine.hasOnTaskCreateTrigger(a.ExecutorKey)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("enabled on_task_create row keyed by Agent.Key was not found through ExecutorKey")
	}
}

func TestRestoredPreAgentConversationDoesNotReplayTaskCreate(t *testing.T) {
	m, s, original, a, profileID := testCustomExecutorSetupWithPlannerControl(t, false)
	const marker = "restored-task-create-must-not-replay"
	if _, err := m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: a.Key, Enabled: true, OnTaskCreate: true, TaskCreateMessage: marker,
	}); err != nil {
		t.Fatal(err)
	}
	sid := seedIntentID(t, original)
	provider := &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "restored conversation done"},
	}}
	installProfileChatAgentForTest(s, profileID, provider)
	wireConversationToolsForTest(t, m)

	// 模拟进程重启后的 LoadExisting：丢弃内存句柄，但保留同一 PG 任务与探索图。
	m.mu.Lock()
	delete(m.tasks, original.ID)
	m.mu.Unlock()
	s.restoreTaskRuntimes()
	restored, ok := m.Task(original.ID)
	if !ok || restored == nil {
		t.Fatal("persisted pre_agent task was not restored")
	}
	if !restored.PreAgentRestored {
		t.Fatal("LoadExisting must mark restored pre_agent task with PreAgentRestored")
	}
	waitForNodeState(t, restored.Store, sid, "done", 5*time.Second)
	conversation, _ := waitForPreAgentConversation(t, s, restored, a.Key)
	message := preAgentConversationUserMessage(t, s, conversation.ID)
	if strings.Contains(message, marker) || strings.Contains(message, "【本次由任务创建触发】") {
		t.Fatalf("restored pre_agent replayed on_task_create: %q", message)
	}
	if !strings.Contains(message, "【首个意图执行协议】") {
		t.Fatalf("restored task skipped the pre_agent protocol itself: %q", message)
	}
	assertNoTaskActivityForIntent(t, restored, sid)
	stopTaskWithin(t, s, restored.ID)
}

func TestPreAgentConversationManualRerunRecoverableStates(t *testing.T) {
	for _, state := range []string{"blocked", "exhausted", "stopped"} {
		t.Run(state, func(t *testing.T) {
			_, s, task, a, profileID := testCustomExecutorSetupWithPlannerControl(t, false)
			sid := seedIntentID(t, task)
			if err := task.Store.SetIntentState(sid, state); err != nil {
				t.Fatal(err)
			}
			provider := &scriptedLLMProvider{claimIntentID: sid, events: []llm.StreamEvent{
				{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "manual rerun done"},
			}}
			installProfileChatAgentForTest(s, profileID, provider)
			wireConversationToolsForTest(t, s.m)
			workerProvider := &scriptedLLMProvider{}
			setPlannerWorkerForTest(t, s, &scriptedLLMProvider{}, workerProvider)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.engine.Run(ctx, task)

			req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/intents/"+strconv.FormatInt(sid, 10)+"/rerun", nil)
			req.SetPathValue("id", task.ID)
			req.SetPathValue("iid", strconv.FormatInt(sid, 10))
			recorder := httptest.NewRecorder()
			s.rerunIntent(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("manual rerun status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			seed := waitForNodeState(t, task.Store, sid, "done", 5*time.Second)
			if seed.Owner != "exec:recon_scout" {
				t.Fatalf("rerun owner=%q, want exec:recon_scout", seed.Owner)
			}
			waitForPreAgentConversation(t, s, task, a.Key)
			if workerProvider.calls != 0 {
				t.Fatalf("manual rerun used default Worker: calls=%d", workerProvider.calls)
			}
			assertNoTaskActivityForIntent(t, task, sid)
			stopTaskWithin(t, s, task.ID)
		})
	}
}

func TestConversationExecutorToolInjection(t *testing.T) {
	m, _, task, a, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	wireConversationToolsForTest(t, m)
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: taskID})
	resolved := names(agent.ToolResolve(ctx, a.Key, nil))
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

func TestConversationTaskDomainUsesTaskID(t *testing.T) {
	m, _, task, a, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	wireConversationToolsForTest(t, m)
	if err := m.pg.AddAgentToToolBinding(a.Key, []string{"report_finding"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.RemoveAgentFromTool(a.Key, "report_finding") })
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: taskID})
	tool := names(agent.ToolResolve(ctx, a.Key, nil))["report_finding"]
	if tool == nil {
		t.Fatal("report_finding not injected into task conversation")
	}
	name := "conversation-taskid-" + task.ID
	raw := fmt.Sprintf(`{"vulnclass":"test","name":%q,"severity":"low","summary":"task attribution","evidence":"synthetic evidence"}`, name)
	res, err := tool.Call(ctx, json.RawMessage(raw), nil)
	if err != nil || res.IsError {
		t.Fatalf("report_finding failed: err=%v result=%s", err, res.Flatten())
	}
	var gotTaskID int64
	if err := m.pg.QueryRow(`SELECT COALESCE(task_id,0) FROM findings WHERE name=$1 ORDER BY id DESC LIMIT 1`, name).Scan(&gotTaskID); err != nil {
		t.Fatal(err)
	}
	if gotTaskID != taskID {
		t.Fatalf("finding task_id=%d, want %d", gotTaskID, taskID)
	}
}

func TestCustomExecutorNoMatchExitsWithoutDone(t *testing.T) {
	_, _, task, a, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	intent := addNonSeedCustomIntent(t, task, "proposed")
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	reason, _, err := agent.RunCustomExecutor(context.Background(), agent.CustomExecutorConfig{
		Agent: a, Provider: &scriptedLLMProvider{}, TaskID: taskID,
		Intent: intent, Store: task.Store, Goal: "synthetic-goal", WorkDir: t.TempDir(),
		RequireSelfClaim: true,
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
	facts, err := task.Store.FactsYielded(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Fatalf("no-match direct executor wrote placeholder facts: %v", facts)
	}
}

func TestCustomExecutorSelfClaimCompletesDone(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	intent := addNonSeedCustomIntent(t, task, "proposed")
	provider := &scriptedLLMProvider{claimIntentID: intent.ID, events: []llm.StreamEvent{
		{Type: llm.SEMessageStart},
		{Type: llm.SETextDelta, Text: "synthetic-self-claim-done"},
	}}
	s.engine.providerFn = func(int64) (llm.Provider, agent.Config, bool) {
		return provider, agent.Config{Model: "synthetic-custom"}, true
	}
	if claimed := s.engine.runIntent(context.Background(), task, "exec:recon_scout", nil, intent, "", "", false); !claimed {
		t.Fatal("direct self-claim runIntent should complete the proposed non-seed intent")
	}
	n, err := task.Store.GetNode(intent.ID)
	if err != nil || n == nil {
		t.Fatalf("reload completed intent: %v", err)
	}
	if n.State != "done" || n.Owner != "exec:recon_scout" {
		t.Fatalf("self-claimed intent state=%s owner=%s, want done/exec:recon_scout", n.State, n.Owner)
	}
}

func TestCustomExecutorEnginePreclaimedRunsWithoutSelfClaim(t *testing.T) {
	_, s, task, _, _ := testCustomExecutorSetupWithPlannerControl(t, false)
	if err := task.Store.SetIntentState(seedIntentID(t, task), "done"); err != nil {
		t.Fatal(err)
	}
	addNonSeedCustomIntent(t, task, "open")
	provider := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "preclaimed execution done"},
	}}
	s.engine.providerFn = func(int64) (llm.Provider, agent.Config, bool) {
		return provider, agent.Config{Model: "synthetic-custom"}, true
	}

	intent, name, customPreclaimed := s.engine.claimNext(task, "work#1", nil)
	if intent == nil {
		t.Fatal("claimNext did not return the non-seed custom intent")
	}
	if intent.Origin == "seed" {
		t.Fatal("claimNext returned the seed instead of the non-seed custom intent")
	}
	if name != "exec:recon_scout" || !customPreclaimed {
		t.Fatalf("claimNext name=%q customPreclaimed=%v, want exec:recon_scout/true", name, customPreclaimed)
	}
	if settled := s.engine.runIntent(context.Background(), task, name, nil, intent, "", "", customPreclaimed); !settled {
		t.Fatal("Engine-preclaimed non-seed intent should execute without self-claim")
	}
	n, err := task.Store.GetNode(intent.ID)
	if err != nil || n == nil {
		t.Fatalf("reload preclaimed intent: %+v err=%v", n, err)
	}
	if n.State != "done" || n.Owner != "exec:recon_scout" {
		t.Fatalf("preclaimed intent state=%s owner=%q, want done/exec:recon_scout", n.State, n.Owner)
	}
	if provider.calls != 1 {
		t.Fatalf("preclaimed provider calls=%d, want 1 without list/claim round trip", provider.calls)
	}
}
