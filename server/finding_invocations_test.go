// finding_invocations_test.go：planner 显式派发 finding 给自定义 Agent 的行为测试。
// 复用 finding_handoff_test.go 的 NewManager + db.DSN harness。队列 fixture 用已取消的
// s.ctx 和非 nil dummy chatAgent：入队即接受但不启动后台运行，队列项可精确计数；
// 直接 runTriggeredRun 在 canceled context 退出前仍创建会话并回填 invocation。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"artex/agent"
	"artex/db"
)

// fiFixture 建一个带持久 PG 的 Manager 和一个“接受但不执行”的触发队列 fixture。
func fiFixture(t *testing.T) (*Manager, *Server) {
	t.Helper()
	if _, _, err := db.DSN(); err != nil {
		t.Skip("test postgres not configured")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pumpLocked 见到 canceled ctx 直接返回：入队可计数，不启动后台 run
	s := &Server{
		m:      m,
		engine: NewEngine(m),
		ctx:    ctx,
		// 非 nil dummy：startTriggeredRun 的队列可用性检查通过；canceled ctx 使
		// runConversationTurn 在调用 Chat 之前退出，dummy 永远不会被真正调用。
		chatAgent:     &agent.ChatAgent{},
		chatBusy:      map[string]bool{},
		chatCancel:    map[string]context.CancelCauseFunc{},
		triggerQ:      map[string][]triggeredRun{},
		triggerActive: map[string]int{},
		triggerCfg:    map[string]triggerBehavior{},
	}
	return m, s
}

// fiPlannerCtx 附加 planner RunInfo（TaskID=0 的场景直接用 context.Background()）。
func fiPlannerCtx(taskID int64) context.Context {
	return agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: taskID})
}

// fiCreateTask 创建一个任务并注册级联清理（findings / invocations 随任务删除）。
func fiCreateTask(t *testing.T, m *Manager, desc string) *Task {
	t.Helper()
	task, err := m.CreateTaskWithOptions(desc, "goal：验证 planner 显式派发 finding", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := mustTaskID(t, task.ID)
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, id) })
	return task
}

// fiCreateFinding 在任务下建一个 getshell/critical finding（带独立测试资产）。
func fiCreateFinding(t *testing.T, m *Manager, task *Task, summary string) (int64, *db.DBFinding) {
	t.Helper()
	taskID := mustTaskID(t, task.ID)
	ip := fmt.Sprintf("192.0.2.%d", 1+time.Now().UnixNano()%200)
	assetID, err := m.pg.Assets().UpsertIP(db.UpsertIPReq{IP: ip, TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := task.Store.AddNode("finding", map[string]any{
		"summary": summary, "vulnclass": "getshell", "severity": "critical",
	}, 0, "confirmed", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	findingID, err := m.pg.AddFinding(taskID, nodeID, "getshell", "SSH 弱口令", "critical",
		summary, "paramiko 连接成功，id 返回 uid=0", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	finding, err := m.pg.GetFinding(findingID)
	if err != nil || finding == nil {
		t.Fatalf("读取 finding %d 失败: %v", findingID, err)
	}
	return findingID, finding
}

// fiCreateAgent 建一个启用的自定义 Agent；triggerMessage 非空时挂一条禁用的
// finding trigger（仅提供 FindingMessage，不干扰调度器）。
func fiCreateAgent(t *testing.T, m *Manager, key, triggerMessage string) {
	t.Helper()
	// 先清理同 key 残留（上一次中断的测试运行可能留下），保证 CreateAgent 不撞唯一约束。
	_, _ = m.pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, key)
	_, _ = m.pg.Exec(`DELETE FROM conversations WHERE agent_key=$1`, key)
	_ = m.pg.DeleteAgent(key)
	if _, err := m.pg.CreateAgent(key, key+" 显示名", "finding_invocations 测试 agent"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, key)
		_, _ = m.pg.Exec(`DELETE FROM conversations WHERE agent_key=$1`, key)
		_ = m.pg.DeleteAgent(key)
	})
	if triggerMessage == "" {
		return
	}
	if _, err := m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: key, Enabled: false, FindingMessage: triggerMessage,
	}); err != nil {
		t.Fatal(err)
	}
}

// fiDispatch 调用 dispatch_finding_to_agent 工具，返回 (Flatten 文本, 是否工具错误)。
func fiDispatch(t *testing.T, s *Server, ctx context.Context, agentKey string, findingID int64, reason string) (string, bool) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"agent_key": agentKey, "finding_id": findingID, "reason": reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.toolDispatchFindingToAgent().Call(ctx, payload, nil)
	if err != nil {
		t.Fatalf("dispatch_finding_to_agent 调用失败: %v", err)
	}
	return res.Flatten(), res.IsError
}

func fiQueueLen(s *Server, agentKey string) int {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return len(s.triggerQ[agentKey])
}

func fiQueueTotal(s *Server) int {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	n := 0
	for _, q := range s.triggerQ {
		n += len(q)
	}
	return n
}

// fiPopRun 取走某 agent 队列头项，便于直接驱动 runTriggeredRun。
func fiPopRun(t *testing.T, s *Server, agentKey string) triggeredRun {
	t.Helper()
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	q := s.triggerQ[agentKey]
	if len(q) == 0 {
		t.Fatalf("agent %s 的触发队列为空", agentKey)
	}
	item := q[0]
	s.triggerQ[agentKey] = q[1:]
	return item
}

func fiInvocationCount(t *testing.T, m *Manager, findingID int64) int {
	t.Helper()
	var n int
	if err := m.pg.QueryRow(`SELECT count(*) FROM finding_agent_invocations WHERE finding_id=$1`, findingID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func fiConversationID(t *testing.T, m *Manager, agentKey string, findingID int64) *int64 {
	t.Helper()
	var id *int64
	if err := m.pg.QueryRow(`SELECT conversation_id FROM finding_agent_invocations WHERE agent_key=$1 AND finding_id=$2`,
		agentKey, findingID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestDispatchFindingToAgentRejectsInvalidCalls 覆盖所有拒绝分支：无记录、无入队。
func TestDispatchFindingToAgentRejectsInvalidCalls(t *testing.T) {
	m, s := fiFixture(t)
	task := fiCreateTask(t, m, "fi 拒绝测试任务")
	taskID := mustTaskID(t, task.ID)
	findingID, _ := fiCreateFinding(t, m, task, "fi 拒绝测试 finding")
	otherTask := fiCreateTask(t, m, "fi 另一任务")
	otherFindingID, _ := fiCreateFinding(t, m, otherTask, "fi 跨任务 finding")
	fiCreateAgent(t, m, "fitest_disabled", "")
	if _, err := m.pg.Exec(`UPDATE agents SET enabled=false WHERE key='fitest_disabled'`); err != nil {
		t.Fatal(err)
	}

	rejected := []struct {
		name    string
		ctx     context.Context
		key     string
		finding int64
		reason  string
		wantErr string
	}{
		{name: "无 planner 任务上下文", ctx: context.Background(), key: "fitest_disabled", finding: findingID, reason: "r", wantErr: "缺少 planner 任务上下文"},
		{name: "空 agent_key", ctx: fiPlannerCtx(taskID), key: "   ", finding: findingID, reason: "r", wantErr: "必填"},
		{name: "不存在的 agent key", ctx: fiPlannerCtx(taskID), key: "fitest_no_such_agent", finding: findingID, reason: "r", wantErr: "目标 Agent 不存在"},
		{name: "builtin worker 目标", ctx: fiPlannerCtx(taskID), key: "worker", finding: findingID, reason: "r", wantErr: "内置"},
		{name: "builtin planner 目标", ctx: fiPlannerCtx(taskID), key: "planner", finding: findingID, reason: "r", wantErr: "内置"},
		{name: "空 reason", ctx: fiPlannerCtx(taskID), key: "fitest_disabled", finding: findingID, reason: "  ", wantErr: "必填"},
		{name: "跨任务 finding", ctx: fiPlannerCtx(taskID), key: "fitest_disabled", finding: otherFindingID, reason: "r", wantErr: "不属于当前 planner 任务"},
		{name: "disabled agent", ctx: fiPlannerCtx(taskID), key: "fitest_disabled", finding: findingID, reason: "r", wantErr: "已禁用"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			out, isErr := fiDispatch(t, s, tc.ctx, tc.key, tc.finding, tc.reason)
			if !isErr {
				t.Fatalf("应拒绝但返回成功: %s", out)
			}
			if !strings.Contains(out, tc.wantErr) {
				t.Fatalf("错误信息应包含 %q，实际: %s", tc.wantErr, out)
			}
			// 拒绝必须不留下调用记录。
			if n := fiInvocationCount(t, m, tc.finding); n != 0 {
				t.Fatalf("拒绝后不应有调用记录，实际 %d 条", n)
			}
			// 拒绝必须不入队。
			if tc.key == "worker" || tc.key == "planner" {
				return // 内置 key 不检查队列
			}
			if got := fiQueueLen(s, tc.key); got != 0 {
				t.Fatalf("拒绝后队列应为空，实际 %d 条", got)
			}
		})
	}
	if total := fiQueueTotal(s); total != 0 {
		t.Fatalf("全部拒绝场景后队列必须为空，实际 %d 条", total)
	}

	// 不存在的 trace_cleaner：只报错，绝不自动创建 agent。
	if existing, err := m.pg.GetAgentByKey("trace_cleaner"); err != nil {
		t.Fatal(err)
	} else if existing != nil {
		if existing.Builtin {
			t.Skip("trace_cleaner 是内置 agent，无法验证不存在分支")
		}
		// 测试库里的 trace_cleaner 可能来自代码 seed、历史数据或手工创建；
		// 删除以验证“不存在”分支（migration 对它缺失时是跳过，不受影响）。
		if err := m.pg.DeleteAgent("trace_cleaner"); err != nil {
			t.Fatal(err)
		}
	}
	out, isErr := fiDispatch(t, s, fiPlannerCtx(taskID), "trace_cleaner", findingID, "投放事实已确认")
	if !isErr || !strings.Contains(out, "目标 Agent 不存在") {
		t.Fatalf("不存在的 trace_cleaner 应报不存在错误: %s", out)
	}
	if a, err := m.pg.GetAgentByKey("trace_cleaner"); err != nil {
		t.Fatal(err)
	} else if a != nil {
		t.Fatal("工具不得自动创建 trace_cleaner agent")
	}
	if n := fiInvocationCount(t, m, findingID); n != 0 {
		t.Fatalf("trace_cleaner 拒绝后不应有调用记录，实际 %d 条", n)
	}
}

// TestDispatchFindingToAgentEnqueuesAndDedupes：有效调用入队一次；重复返回
// already_invoked 且队列不增长；同 finding 的另一 agent 有独立记录；任意
// 非 dispatcher/cleaner 的自定义 Agent 都能作为目标，证明代码没有场景白名单。
func TestDispatchFindingToAgentEnqueuesAndDedupes(t *testing.T) {
	m, s := fiFixture(t)
	task := fiCreateTask(t, m, "fi 入队测试任务")
	taskID := mustTaskID(t, task.ID)
	findingID, _ := fiCreateFinding(t, m, task, "fi 入队 finding")
	fiCreateAgent(t, m, "fitest_alpha", "")
	fiCreateAgent(t, m, "fitest_beta", "")

	out, isErr := fiDispatch(t, s, fiPlannerCtx(taskID), "fitest_alpha", findingID, "已立足且拿到 shell")
	if isErr || !strings.Contains(out, `"status":"enqueued"`) {
		t.Fatalf("首次调用应入队: err=%v out=%s", isErr, out)
	}
	if got := fiQueueLen(s, "fitest_alpha"); got != 1 {
		t.Fatalf("首次调用后队列应为 1 条，实际 %d", got)
	}
	if id := fiConversationID(t, m, "fitest_alpha", findingID); id != nil {
		t.Fatalf("会话创建前 conversation_id 应为 NULL，实际 %v", *id)
	}

	// Conversation 创建并回填后，第二次调用才返回 already_invoked；队列不增长。
	s.runTriggeredRun(fiPopRun(t, s, "fitest_alpha"))
	conversationID := fiConversationID(t, m, "fitest_alpha", findingID)
	if conversationID == nil {
		t.Fatal("首轮运行后应回填 conversation_id")
	}
	out, isErr = fiDispatch(t, s, fiPlannerCtx(taskID), "fitest_alpha", findingID, "再次派发")
	if isErr || !strings.Contains(out, `"status":"already_invoked"`) ||
		!strings.Contains(out, fmt.Sprintf(`"conversation_id":%d`, *conversationID)) {
		t.Fatalf("重复调用应返回 already_invoked 和首次会话 ID: err=%v out=%s", isErr, out)
	}
	if got := fiQueueLen(s, "fitest_alpha"); got != 0 {
		t.Fatalf("重复调用后队列不应增长，实际 %d", got)
	}

	// 同一 finding 派发给另一个 agent：独立记录、独立入队。
	out, isErr = fiDispatch(t, s, fiPlannerCtx(taskID), "fitest_beta", findingID, "另一目标")
	if isErr || !strings.Contains(out, `"status":"enqueued"`) {
		t.Fatalf("另一 agent 的首次调用应入队: err=%v out=%s", isErr, out)
	}
	if got := fiQueueLen(s, "fitest_beta"); got != 1 {
		t.Fatalf("fitest_beta 队列应为 1 条，实际 %d", got)
	}
	// fitest_alpha / fitest_beta 都是普通自定义 Agent（非 dispatcher/cleaner 命名），
	// 能入队即证明工具只校验“存在、启用、非 builtin”，没有场景白名单。
	if n := fiInvocationCount(t, m, findingID); n != 2 {
		t.Fatalf("同一 finding 应有两条独立调用记录，实际 %d", n)
	}
}

func TestDispatchFindingToTraceCleaner(t *testing.T) {
	m, s := fiFixture(t)
	task := fiCreateTask(t, m, "fi Cleaner 派发任务")
	taskID := mustTaskID(t, task.ID)
	findingID, _ := fiCreateFinding(t, m, task, "fi 已立足且存在投放事实")
	fiCreateAgent(t, m, "trace_cleaner", "仅核对投放事实")

	out, isErr := fiDispatch(t, s, fiPlannerCtx(taskID), "trace_cleaner", findingID, "立足点可用且存在投放事实")
	if isErr || !strings.Contains(out, `"status":"enqueued"`) {
		t.Fatalf("Planner 应能显式派发 trace_cleaner: err=%v out=%s", isErr, out)
	}
	item := fiPopRun(t, s, "trace_cleaner")
	if item.agentKey != "trace_cleaner" || len(item.invocations) != 1 {
		t.Fatalf("Cleaner 队列项错误: %+v", item)
	}
	s.runTriggeredRun(item)
	conversationID := fiConversationID(t, m, "trace_cleaner", findingID)
	if conversationID == nil {
		t.Fatal("Cleaner Conversation 创建后应回填 conversation_id")
	}
	var agentKey string
	if err := m.pg.QueryRow(`SELECT agent_key FROM conversations WHERE id=$1`, *conversationID).Scan(&agentKey); err != nil {
		t.Fatal(err)
	}
	if agentKey != "trace_cleaner" {
		t.Fatalf("创建的会话不属于 trace_cleaner: %q", agentKey)
	}
}

// TestDispatchFindingRetryScheduledWithoutChatAgent：chat agent 不可用时首次
// 记录仍写入，状态 retry_scheduled 且 conversation_id 为 NULL、不入队。
func TestDispatchFindingRetryScheduledWithoutChatAgent(t *testing.T) {
	m, s := fiFixture(t)
	s.chatAgent = nil // 模拟 LLM 未配置
	task := fiCreateTask(t, m, "fi 无 chat agent 任务")
	taskID := mustTaskID(t, task.ID)
	findingID, _ := fiCreateFinding(t, m, task, "fi 等待补发 finding")
	fiCreateAgent(t, m, "fitest_nochat", "")

	out, isErr := fiDispatch(t, s, fiPlannerCtx(taskID), "fitest_nochat", findingID, "队列不可用")
	if isErr || !strings.Contains(out, `"status":"retry_scheduled"`) {
		t.Fatalf("chat agent 不可用时首次调用应返回 retry_scheduled: err=%v out=%s", isErr, out)
	}
	if id := fiConversationID(t, m, "fitest_nochat", findingID); id != nil {
		t.Fatalf("conversation_id 应为 NULL，实际 %v", *id)
	}
	if got := fiQueueLen(s, "fitest_nochat"); got != 0 {
		t.Fatalf("队列不可用时不应入队，实际 %d 条", got)
	}
	// 待补发记录存在：同一调用再来一次不得重复入队。
	out, isErr = fiDispatch(t, s, fiPlannerCtx(taskID), "fitest_nochat", findingID, "再次调用")
	if isErr || !strings.Contains(out, `"status":"retry_scheduled"`) {
		t.Fatalf("待补发状态重复调用应返回 retry_scheduled: err=%v out=%s", isErr, out)
	}
}

// TestDispatchFindingMessageAlwaysCarriesStructuredFields：trigger message 不含
// 任何占位符时，实际消息仍必须包含 class/severity/summary/FindingID/AssetIDs/
// EvidenceVersion 等结构化字段（不能依赖占位符）。
func TestDispatchFindingMessageAlwaysCarriesStructuredFields(t *testing.T) {
	m, s := fiFixture(t)
	task := fiCreateTask(t, m, "fi 消息构造任务")
	taskID := mustTaskID(t, task.ID)
	summary := "fi 消息构造 finding 摘要"
	findingID, finding := fiCreateFinding(t, m, task, summary)
	// 故意不含 __VULNCLASS__/__SEVERITY__/__SUMMARY__ 占位符。
	fiCreateAgent(t, m, "fitest_nomsg", "固定触发正文：无占位符，请评估处置。")

	out, isErr := fiDispatch(t, s, fiPlannerCtx(taskID), "fitest_nomsg", findingID, "证据已复核")
	if isErr || !strings.Contains(out, `"status":"enqueued"`) {
		t.Fatalf("调用应入队: err=%v out=%s", isErr, out)
	}
	item := fiPopRun(t, s, "fitest_nomsg")
	msg := finalTriggerMessage(item)
	for _, want := range []string{
		"固定触发正文：无占位符，请评估处置。", // trigger message 原文保留
		"【本次由 planner 确认处置前提后显式派发】",
		"VulnClass: " + finding.VulnClass,
		"Severity: " + finding.Severity,
		"Summary: " + summary,
		fmt.Sprintf("FindingID: %d", findingID),
		fmt.Sprintf("AssetIDs: %v", finding.AssetIDs),
		fmt.Sprintf("EvidenceVersion: %d", finding.EvidenceVersion),
		"execution_mode: ",
		"PlannerReason: 证据已复核",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("消息缺少 %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "__VULNCLASS__") || strings.Contains(msg, "__SEVERITY__") || strings.Contains(msg, "__SUMMARY__") {
		t.Fatalf("消息不应残留占位符:\n%s", msg)
	}
	if !strings.Contains(item.title, fmt.Sprintf("task#%d", taskID)) || !strings.Contains(item.title, fmt.Sprintf("finding#%d", findingID)) {
		t.Fatalf("标题应包含 task/finding 编号: %q", item.title)
	}
	// 单条派发携带一个 invocation ref，供会话创建后回填。
	if len(item.invocations) != 1 || item.invocations[0] != (findingInvocationRef{agentKey: "fitest_nomsg", findingID: findingID}) {
		t.Fatalf("队列项应携带该 finding 的 invocation ref: %+v", item.invocations)
	}
}

// TestRunTriggeredRunBackfillsInvocationConversation：直接驱动 runTriggeredRun
// 创建会话并回填；合并两个 finding 的 run 回填两行且指向同一会话。
func TestRunTriggeredRunBackfillsInvocationConversation(t *testing.T) {
	m, s := fiFixture(t)
	task := fiCreateTask(t, m, "fi 回填任务")
	taskID := mustTaskID(t, task.ID)
	findingA, _ := fiCreateFinding(t, m, task, "fi 回填 finding A")
	findingB, _ := fiCreateFinding(t, m, task, "fi 回填 finding B")
	fiCreateAgent(t, m, "fitest_backfill", "")

	// 单 finding：dispatch 入队 → 直接 runTriggeredRun → 行内写入会话 ID。
	if _, isErr := fiDispatch(t, s, fiPlannerCtx(taskID), "fitest_backfill", findingA, "首轮派发"); isErr {
		t.Fatal("dispatch 不应报错")
	}
	item := fiPopRun(t, s, "fitest_backfill")
	s.runTriggeredRun(item)
	convID := fiConversationID(t, m, "fitest_backfill", findingA)
	if convID == nil {
		t.Fatal("runTriggeredRun 后 conversation_id 应已回填")
	}
	var convAgent string
	if err := m.pg.QueryRow(`SELECT agent_key FROM conversations WHERE id=$1`, *convID).Scan(&convAgent); err != nil {
		t.Fatalf("回填的会话应存在: %v", err)
	}
	if convAgent != "fitest_backfill" {
		t.Fatalf("会话 agent_key 不匹配: %q", convAgent)
	}

	// 两个尚未创建会话的 finding 合并 run：两行都回填，且指向同一会话。
	findingC, _ := fiCreateFinding(t, m, task, "fi 回填 finding C")
	if _, _, err := m.pg.RecordFindingInvocation("fitest_backfill", findingB, taskID, "第二个 finding"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.pg.RecordFindingInvocation("fitest_backfill", findingC, taskID, "第三个 finding"); err != nil {
		t.Fatal(err)
	}
	merged := mergeTriggeredRuns([]triggeredRun{
		{agentKey: "fitest_backfill", title: "t1", message: "body-1", taskID: taskID, mergeable: true,
			invocations: []findingInvocationRef{{agentKey: "fitest_backfill", findingID: findingB}}},
		{agentKey: "fitest_backfill", title: "t2", message: "body-2", taskID: taskID, mergeable: true,
			invocations: []findingInvocationRef{{agentKey: "fitest_backfill", findingID: findingC}}},
	})
	if len(merged.invocations) != 2 {
		t.Fatalf("合并 run 应携带 2 条 ref，实际 %d", len(merged.invocations))
	}
	s.runTriggeredRun(merged)
	idB := fiConversationID(t, m, "fitest_backfill", findingB)
	idC := fiConversationID(t, m, "fitest_backfill", findingC)
	if idB == nil || idC == nil {
		t.Fatalf("合并 run 后两行都应回填: B=%v C=%v", idB, idC)
	}
	if *idB != *idC {
		t.Fatalf("合并 run 的两行回填必须指向同一会话: B=%d C=%d", *idB, *idC)
	}
	if *idB == *convID {
		t.Fatalf("合并 run 应创建新会话，不应复用首轮会话 %d", *convID)
	}
}

// TestRetryFindingInvocationsRequeuesOnceAndWritesLastRetry：NULL 记录 11 分钟后
// 由兜底补发一次并写 last_retry_at；立即再调用不新增；已有 conversation 的对照行不处理。
func TestRetryFindingInvocationsRequeuesOnceAndWritesLastRetry(t *testing.T) {
	m, s := fiFixture(t)
	task := fiCreateTask(t, m, "fi 兜底补发任务")
	taskID := mustTaskID(t, task.ID)
	findingDue, _ := fiCreateFinding(t, m, task, "fi 待补发 finding")
	findingDone, _ := fiCreateFinding(t, m, task, "fi 已建会话 finding")
	fiCreateAgent(t, m, "fitest_due", "")
	fiCreateAgent(t, m, "fitest_done", "")

	if _, _, err := m.pg.RecordFindingInvocation("fitest_due", findingDue, taskID, "服务重启前派发"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.pg.RecordFindingInvocation("fitest_done", findingDone, taskID, "已建会话"); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetFindingInvocationConversation("fitest_done", findingDone, 424242); err != nil {
		t.Fatal(err) // conversation_id 无外键：任意已删会话 ID 视为已启动
	}
	// 只把待补发行改到 11 分钟前（对照行的 created_at 保持当前）。
	if _, err := m.pg.Exec(`UPDATE finding_agent_invocations SET created_at=now()-interval '11 minutes'
		WHERE agent_key='fitest_due' AND finding_id=$1`, findingDue); err != nil {
		t.Fatal(err)
	}

	sc := newScheduler(s)
	now := time.Now()
	sc.retryFindingInvocations(now)

	if got := fiQueueLen(s, "fitest_due"); got != 1 {
		t.Fatalf("兜底应补发 1 个队列项，实际 %d", got)
	}
	var lastRetry *time.Time
	if err := m.pg.QueryRow(`SELECT last_retry_at FROM finding_agent_invocations WHERE agent_key='fitest_due' AND finding_id=$1`,
		findingDue).Scan(&lastRetry); err != nil {
		t.Fatal(err)
	}
	if lastRetry == nil {
		t.Fatal("认领后必须写入 last_retry_at")
	}
	// 立即再调用：10 分钟时间门 + 行级节流，队列不增长。
	sc.retryFindingInvocations(now.Add(time.Second))
	if got := fiQueueLen(s, "fitest_due"); got != 1 {
		t.Fatalf("立即再调用不得新增队列项，实际 %d", got)
	}
	// 有 conversation 的对照行：不处理、不补发。
	if got := fiQueueLen(s, "fitest_done"); got != 0 {
		t.Fatalf("已回填 conversation 的行不得补发，实际 %d 条", got)
	}
	if id := fiConversationID(t, m, "fitest_done", findingDone); id == nil || *id != 424242 {
		t.Fatalf("对照行的 conversation_id 不应被改动: %v", id)
	}
}

// TestStepRequeuesDueInvocationWithoutEnabledTriggers：数据库没有任何 enabled
// trigger 时 step() 仍补发 due invocation —— 兜底必须位于 len(triggers)==0 提前返回之前。
func TestStepRequeuesDueInvocationWithoutEnabledTriggers(t *testing.T) {
	m, s := fiFixture(t)
	task := fiCreateTask(t, m, "fi step 兜底任务")
	taskID := mustTaskID(t, task.ID)
	findingID, _ := fiCreateFinding(t, m, task, "fi step 兜底 finding")
	fiCreateAgent(t, m, "fitest_step", "")

	// 快照并禁用全部 enabled trigger，结束时恢复，确保“没有任何 enabled trigger”。
	rows, err := m.pg.Query(`SELECT id FROM agent_triggers WHERE enabled`)
	if err != nil {
		t.Fatal(err)
	}
	var enabledIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		enabledIDs = append(enabledIDs, id)
	}
	rows.Close()
	if _, err := m.pg.Exec(`UPDATE agent_triggers SET enabled=false WHERE enabled`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range enabledIDs {
			_, _ = m.pg.Exec(`UPDATE agent_triggers SET enabled=true WHERE id=$1`, id)
		}
	})
	var enabledNow int
	if err := m.pg.QueryRow(`SELECT count(*) FROM agent_triggers WHERE enabled`).Scan(&enabledNow); err != nil {
		t.Fatal(err)
	}
	if enabledNow != 0 {
		t.Fatalf("前置条件失败：仍有 %d 条 enabled trigger", enabledNow)
	}

	if _, _, err := m.pg.RecordFindingInvocation("fitest_step", findingID, taskID, "服务重启丢失队列"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`UPDATE finding_agent_invocations SET created_at=now()-interval '11 minutes'
		WHERE agent_key='fitest_step' AND finding_id=$1`, findingID); err != nil {
		t.Fatal(err)
	}

	sc := newScheduler(s)
	sc.step() // 第一行就是 retryFindingInvocations；ListEnabledTriggers 为空也不能提前拦住它

	if got := fiQueueLen(s, "fitest_step"); got != 1 {
		t.Fatalf("step() 在零 enabled trigger 时仍应补发 due invocation 一次，实际 %d 条", got)
	}
	var lastRetry *time.Time
	if err := m.pg.QueryRow(`SELECT last_retry_at FROM finding_agent_invocations WHERE agent_key='fitest_step' AND finding_id=$1`,
		findingID).Scan(&lastRetry); err != nil {
		t.Fatal(err)
	}
	if lastRetry == nil {
		t.Fatal("step() 补发后应写入 last_retry_at")
	}
}
