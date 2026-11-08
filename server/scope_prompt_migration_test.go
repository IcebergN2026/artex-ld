package server

import (
	"fmt"
	"testing"
	"time"

	"artex/agent"
	db "artex/db"
)

type scopeRollbackPromptFingerprint struct {
	count   int
	current string
}

// TestUpgradeScopeAuthorizationPrompts 验证严格范围回滚迁移：新 key 独立于
// unrestricted_agent_prompts_v2；系统默认模板/提示词恢复，自定义提示词与任务快照不动；
// goals 保持上游原生；重复执行无副作用。
func TestUpgradeScopeAuthorizationPrompts(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })

	t.Cleanup(stashSettingFlag(t, pg, scopeAuthorizationPromptMigrationKey))
	t.Cleanup(stashSettingFlag(t, pg, "unrestricted_agent_prompts_v2"))
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, scopeAuthorizationPromptMigrationKey); err != nil {
		t.Fatal(err)
	}
	// 模拟存量库：旧无范围限制迁移早已完成；新 key 不存在时必须仍执行回滚。
	if err := pg.SetSetting("unrestricted_agent_prompts_v2", "done"); err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	plannerSeeds := agent.PlannerDomainSeeds()
	workerSeedList := agent.WorkerDomainSeeds()
	workerSeeds := make(map[string]string, len(workerSeedList))
	for _, seed := range workerSeedList {
		workerSeeds[seed.NKey] = seed.Body
	}
	// 把回滚迁移应命中的内置场景模板置为「无范围限制」旧正文。
	for nkey := range plannerSeeds {
		forceScopeRollbackPlannerBody(t, pg, nkey, "旧无范围限制 planner 模板 "+nkey)
	}
	for nkey := range workerSeeds {
		forceScopeRollbackWorkerBody(t, pg, nkey, "旧无范围限制 worker 模板 "+nkey)
	}

	customPlannerBody := "自定义 Planner 模板：回滚迁移不得覆盖。"
	customWorkerBody := "自定义 Worker 模板：回滚迁移不得覆盖。"
	customPlanner, err := pg.CreatePlannerTemplate(db.PlannerTemplateInput{
		Name: fmt.Sprintf("scope-rollback-custom-planner-%d", stamp), Body: customPlannerBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM planner_templates WHERE id=$1`, customPlanner.ID) })
	customWorker, err := pg.CreateWorkerTemplate(db.WorkerTemplateInput{
		Name: fmt.Sprintf("scope-rollback-custom-worker-%d", stamp), Body: customWorkerBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM worker_templates WHERE id=$1`, customWorker.ID) })

	seeds := agent.BuiltinPromptSeeds()
	plannerAgent := forceScopeRollbackPrompt(t, pg, "planner", "旧无范围限制 planner 系统提示词", "system")
	workerAgent := forceScopeRollbackPrompt(t, pg, "worker", "旧无范围限制 worker 系统提示词", "system")
	pentestAgent := forceScopeRollbackPrompt(t, pg, "pentest", "旧无范围限制 pentest 系统提示词", "system")
	mainAgent := forceScopeRollbackPrompt(t, pg, "mainagent", "旧无范围限制 mainagent 系统提示词", "system")
	reviewAgent := forceScopeRollbackPrompt(t, pg, "review", "旧无范围限制 review 系统提示词", "system")
	// auto 模拟用户改过的当前提示词：即使它是被无范围限制迁移改过 key 之一，也不得覆盖。
	autoCustomBody := "用户自定义 auto 提示词：回滚迁移不得覆盖。"
	autoAgent := forceScopeRollbackPrompt(t, pg, "auto", autoCustomBody, "ui")
	goalsBody := "上游原生 goals 提示词：回滚迁移不得触碰。"
	goalsAgent := forceScopeRollbackPrompt(t, pg, "goals", goalsBody, "system")

	// vuln_dispatcher 是系统预置的 builtin=false agent；存量库可能已存在，也可能需在测试里补建。
	// 新建时 ensureScopeRollbackAgent 自己注册删除清理，随后 forceScopeRollbackPrompt 的恢复清理会先执行。
	if _, created := ensureScopeRollbackAgent(t, pg, "vuln_dispatcher", "漏洞派发", "scope rollback test"); created {
		t.Log("测试库缺少 vuln_dispatcher，已临时创建")
	}
	dispatcherAgent := forceScopeRollbackPrompt(t, pg, "vuln_dispatcher", "旧无范围限制 dispatcher 系统提示词", "system")

	// 任务快照没有可靠血统元数据：迁移必须一个都不改。
	task, err := pg.CreateTask(fmt.Sprintf("scope rollback snapshot task %d", stamp), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pg.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = pg.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID)
	})
	snapPlanner := "任务级自定义 Planner 快照：迁移不得修改。"
	snapWorker := "任务级自定义 Worker 快照：迁移不得修改。"
	if _, err := pg.Exec(`UPDATE tasks SET planner_prompt_override=$1,worker_prompt_override=$2 WHERE id=$3`,
		snapPlanner, snapWorker, task.ID); err != nil {
		t.Fatal(err)
	}
	assertTaskSnapshots := func() {
		t.Helper()
		var gotPlanner, gotWorker string
		if err := pg.QueryRow(`SELECT planner_prompt_override,worker_prompt_override FROM tasks WHERE id=$1`, task.ID).
			Scan(&gotPlanner, &gotWorker); err != nil {
			t.Fatal(err)
		}
		if gotPlanner != snapPlanner || gotWorker != snapWorker {
			t.Fatalf("任务提示词快照被迁移修改: planner=%q worker=%q", gotPlanner, gotWorker)
		}
	}

	plannerBefore := capturePromptState(t, pg, plannerAgent.ID)
	workerBefore := capturePromptState(t, pg, workerAgent.ID)
	pentestBefore := capturePromptState(t, pg, pentestAgent.ID)
	mainBefore := capturePromptState(t, pg, mainAgent.ID)
	reviewBefore := capturePromptState(t, pg, reviewAgent.ID)
	autoBefore := capturePromptState(t, pg, autoAgent.ID)
	goalsBefore := capturePromptState(t, pg, goalsAgent.ID)
	dispatcherBefore := capturePromptState(t, pg, dispatcherAgent.ID)

	if err := upgradeScopeAuthorizationPrompts(pg); err != nil {
		t.Fatal(err)
	}
	if value, ok, err := pg.GetSetting(scopeAuthorizationPromptMigrationKey); err != nil || !ok || value != "done" {
		t.Fatalf("迁移成功标记未写入: value=%q ok=%v err=%v", value, ok, err)
	}
	if value, ok, err := pg.GetSetting("unrestricted_agent_prompts_v2"); err != nil || !ok || value != "done" {
		t.Fatalf("旧无范围限制标记不应被改动: value=%q ok=%v err=%v", value, ok, err)
	}

	assertScopeRollbackTemplates(t, pg, plannerSeeds, workerSeeds)
	if got, err := pg.GetPlannerTemplate(customPlanner.ID); err != nil || got == nil || got.Body != customPlannerBody {
		t.Fatalf("自定义 Planner 模板被迁移修改: %v", err)
	}
	if got, err := pg.GetWorkerTemplate(customWorker.ID); err != nil || got == nil || got.Body != customWorkerBody {
		t.Fatalf("自定义 Worker 模板被迁移修改: %v", err)
	}

	assertScopeRollbackPrompt(t, pg, "planner", plannerAgent.ID, seeds["planner"])
	assertScopeRollbackPrompt(t, pg, "worker", workerAgent.ID, seeds["worker"])
	assertScopeRollbackPrompt(t, pg, "pentest", pentestAgent.ID, seeds["pentest"])
	assertScopeRollbackPrompt(t, pg, "mainagent", mainAgent.ID, seeds["mainagent"])
	assertScopeRollbackPrompt(t, pg, "review", reviewAgent.ID, seeds["review"])
	assertScopeRollbackPrompt(t, pg, "auto (user)", autoAgent.ID, autoCustomBody)
	assertScopeRollbackPrompt(t, pg, "goals", goalsAgent.ID, goalsBody)
	assertScopeRollbackPrompt(t, pg, "vuln_dispatcher", dispatcherAgent.ID, agent.VulnDispatcherDefaultPrompt)
	assertScopeRollbackPromptVersionDelta(t, pg, "planner", plannerBefore, plannerAgent.ID, 1)
	assertScopeRollbackPromptVersionDelta(t, pg, "worker", workerBefore, workerAgent.ID, 1)
	assertScopeRollbackPromptVersionDelta(t, pg, "pentest", pentestBefore, pentestAgent.ID, 1)
	assertScopeRollbackPromptVersionDelta(t, pg, "mainagent", mainBefore, mainAgent.ID, 1)
	assertScopeRollbackPromptVersionDelta(t, pg, "review", reviewBefore, reviewAgent.ID, 1)
	assertScopeRollbackPromptVersionDelta(t, pg, "auto (user)", autoBefore, autoAgent.ID, 0)
	assertScopeRollbackPromptVersionDelta(t, pg, "goals", goalsBefore, goalsAgent.ID, 0)
	assertScopeRollbackPromptVersionDelta(t, pg, "vuln_dispatcher", dispatcherBefore, dispatcherAgent.ID, 1)
	assertTaskSnapshots()

	ids := []int64{plannerAgent.ID, workerAgent.ID, pentestAgent.ID, mainAgent.ID, reviewAgent.ID, autoAgent.ID, goalsAgent.ID, dispatcherAgent.ID}
	captureAll := func() map[int64]scopeRollbackPromptFingerprint {
		out := make(map[int64]scopeRollbackPromptFingerprint, len(ids))
		for _, id := range ids {
			out[id] = captureScopeRollbackPromptFingerprint(t, pg, id)
		}
		return out
	}
	afterFirstRun := captureAll()

	// 标记已存在时再次调用必须直接返回，不追加版本。
	if err := upgradeScopeAuthorizationPrompts(pg); err != nil {
		t.Fatal(err)
	}
	assertScopeRollbackFingerprintsUnchanged(t, afterFirstRun, captureAll())

	// 删除标记让内部逻辑真正重跑一遍：当前提示词/模板已是严格默认，必须无副作用。
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, scopeAuthorizationPromptMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := upgradeScopeAuthorizationPrompts(pg); err != nil {
		t.Fatal(err)
	}
	assertScopeRollbackFingerprintsUnchanged(t, afterFirstRun, captureAll())
	assertScopeRollbackTemplates(t, pg, plannerSeeds, workerSeeds)
	assertScopeRollbackPrompt(t, pg, "auto (user)", autoAgent.ID, autoCustomBody)
	assertTaskSnapshots()

	// builtin=false 的系统预置 dispatcher 也只在 updated_by='system' 时回滚；
	// 用户当前提示词同样必须原样保留。
	dispatcherCustomBody := "用户自定义 dispatcher 提示词：迁移不得覆盖。"
	if _, err := pg.SavePrompt(dispatcherAgent.ID, dispatcherCustomBody, "用户自定义", "ui"); err != nil {
		t.Fatal(err)
	}
	customDispatcherBefore := capturePromptState(t, pg, dispatcherAgent.ID)
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, scopeAuthorizationPromptMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := upgradeScopeAuthorizationPrompts(pg); err != nil {
		t.Fatal(err)
	}
	assertScopeRollbackPrompt(t, pg, "vuln_dispatcher (user)", dispatcherAgent.ID, dispatcherCustomBody)
	if got := capturePromptState(t, pg, dispatcherAgent.ID).count; got != customDispatcherBefore.count {
		t.Fatalf("用户自定义 dispatcher 提示词被迁移追加了版本: before=%d after=%d", customDispatcherBefore.count, got)
	}
	if value, ok, err := pg.GetSetting(scopeAuthorizationPromptMigrationKey); err != nil || !ok || value != "done" {
		t.Fatalf("重跑后成功标记未写入: value=%q ok=%v err=%v", value, ok, err)
	}
}

// TestUpgradeScopeAuthorizationPromptsReturnsError 验证迁移失败会向上返回错误，而不是静默成功。
func TestUpgradeScopeAuthorizationPromptsReturnsError(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.Close(); err != nil {
		t.Fatal(err)
	}
	if err := upgradeScopeAuthorizationPrompts(pg); err == nil {
		t.Fatal("关闭数据库后迁移仍返回 nil，必须返回错误")
	}
}

func forceScopeRollbackPlannerBody(t *testing.T, pg *db.DB, nkey, body string) {
	t.Helper()
	tpl, err := pg.GetPlannerTemplateByNKey(nkey)
	if err != nil {
		t.Fatal(err)
	}
	if tpl == nil {
		inserted, err := pg.SeedPlannerTemplateIfEmpty(nkey, nkey, body, false)
		if err != nil {
			t.Fatal(err)
		}
		tpl, err = pg.GetPlannerTemplateByNKey(nkey)
		if err != nil || tpl == nil {
			t.Fatalf("seed planner template %s: %v", nkey, err)
		}
		if inserted {
			t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM planner_templates WHERE nkey=$1`, nkey) })
		} else {
			registerScopeRollbackPlannerRestore(t, pg, tpl)
		}
	} else {
		registerScopeRollbackPlannerRestore(t, pg, tpl)
	}
	if _, err := pg.Exec(`UPDATE planner_templates SET body=$1,builtin=true WHERE id=$2`, body, tpl.ID); err != nil {
		t.Fatal(err)
	}
}

func registerScopeRollbackPlannerRestore(t *testing.T, pg *db.DB, tpl *db.PlannerTemplate) {
	t.Helper()
	origBody, origBuiltin, origDefault := tpl.Body, tpl.Builtin, tpl.IsDefault
	t.Cleanup(func() {
		_, _ = pg.Exec(`UPDATE planner_templates SET body=$1,builtin=$2,is_default=$3 WHERE id=$4`,
			origBody, origBuiltin, origDefault, tpl.ID)
	})
}

func forceScopeRollbackWorkerBody(t *testing.T, pg *db.DB, nkey, body string) {
	t.Helper()
	tpl, err := pg.GetWorkerTemplateByNKey(nkey)
	if err != nil {
		t.Fatal(err)
	}
	if tpl == nil {
		inserted, err := pg.SeedWorkerTemplateIfEmpty(nkey, nkey, body, false)
		if err != nil {
			t.Fatal(err)
		}
		tpl, err = pg.GetWorkerTemplateByNKey(nkey)
		if err != nil || tpl == nil {
			t.Fatalf("seed worker template %s: %v", nkey, err)
		}
		if inserted {
			t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM worker_templates WHERE nkey=$1`, nkey) })
		} else {
			registerScopeRollbackWorkerRestore(t, pg, tpl)
		}
	} else {
		registerScopeRollbackWorkerRestore(t, pg, tpl)
	}
	if _, err := pg.Exec(`UPDATE worker_templates SET body=$1,builtin=true WHERE id=$2`, body, tpl.ID); err != nil {
		t.Fatal(err)
	}
}

func registerScopeRollbackWorkerRestore(t *testing.T, pg *db.DB, tpl *db.WorkerTemplate) {
	t.Helper()
	origBody, origBuiltin, origDefault := tpl.Body, tpl.Builtin, tpl.IsDefault
	t.Cleanup(func() {
		_, _ = pg.Exec(`UPDATE worker_templates SET body=$1,builtin=$2,is_default=$3 WHERE id=$4`,
			origBody, origBuiltin, origDefault, tpl.ID)
	})
}

func ensureScopeRollbackAgent(t *testing.T, pg *db.DB, key, name, description string) (*db.Agent, bool) {
	t.Helper()
	a, err := pg.GetAgentByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if a != nil {
		return a, false
	}
	a, err = pg.CreateAgent(key, name, description)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteAgent(key) })
	return a, true
}

// forceScopeRollbackPrompt snapshots the current prompt state, then appends a fixture
// version with the given ownership. Cleanup restores the exact previous state.
func forceScopeRollbackPrompt(t *testing.T, pg *db.DB, key, body, updatedBy string) *db.Agent {
	t.Helper()
	a, err := pg.GetAgentByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatalf("agent %s missing", key)
	}
	st := capturePromptState(t, pg, a.ID)
	t.Cleanup(func() { st.restore(pg) })
	if _, err := pg.SavePrompt(a.ID, body, "scope rollback fixture", updatedBy); err != nil {
		t.Fatal(err)
	}
	return a
}

func assertScopeRollbackPrompt(t *testing.T, pg *db.DB, label string, agentID int64, want string) {
	t.Helper()
	got, err := pg.CurrentPrompt(agentID)
	if err != nil {
		t.Fatalf("读取 %s 当前提示词: %v", label, err)
	}
	if got != want {
		t.Fatalf("%s 当前提示词未恢复为严格默认", label)
	}
}

func assertScopeRollbackPromptVersionDelta(t *testing.T, pg *db.DB, label string, before *promptState, agentID int64, wantDelta int) {
	t.Helper()
	after := capturePromptState(t, pg, agentID)
	if after.count != before.count+wantDelta {
		t.Fatalf("%s 提示词版本数变化错误: before=%d after=%d wantDelta=%d", label, before.count, after.count, wantDelta)
	}
}

func captureScopeRollbackPromptFingerprint(t *testing.T, pg *db.DB, agentID int64) scopeRollbackPromptFingerprint {
	t.Helper()
	st := capturePromptState(t, pg, agentID)
	current, err := pg.CurrentPrompt(agentID)
	if err != nil {
		t.Fatal(err)
	}
	return scopeRollbackPromptFingerprint{count: st.count, current: current}
}

func assertScopeRollbackFingerprintsUnchanged(t *testing.T, want, got map[int64]scopeRollbackPromptFingerprint) {
	t.Helper()
	for id, before := range want {
		if got[id] != before {
			t.Fatalf("重跑产生了提示词副作用: agent=%d before=%+v after=%+v", id, before, got[id])
		}
	}
}

func assertScopeRollbackTemplates(t *testing.T, pg *db.DB, plannerSeeds, workerSeeds map[string]string) {
	t.Helper()
	for nkey, want := range plannerSeeds {
		got, err := pg.GetPlannerTemplateByNKey(nkey)
		if err != nil || got == nil {
			t.Fatalf("读取内置 Planner 模板 %s: %v", nkey, err)
		}
		if !got.Builtin || got.Body != want {
			t.Fatalf("内置 Planner 模板 %s 未恢复为严格默认", nkey)
		}
	}
	for nkey, want := range workerSeeds {
		got, err := pg.GetWorkerTemplateByNKey(nkey)
		if err != nil || got == nil {
			t.Fatalf("读取内置 Worker 模板 %s: %v", nkey, err)
		}
		if !got.Builtin || got.Body != want {
			t.Fatalf("内置 Worker 模板 %s 未恢复为严格默认", nkey)
		}
	}
}

func TestScopeRollbackRestoresDefaultDispatcherMessage(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	ensureScopeRollbackAgent(t, pg, "vuln_dispatcher", "漏洞派发", "scope rollback test")

	// Preserve any pre-existing trigger rows on a shared development database.
	existing, err := pg.ListTriggersFor("vuln_dispatcher")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, trigger := range existing {
			_ = pg.UpdateTrigger(trigger)
		}
	})

	defaultTrigger, err := pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: "vuln_dispatcher", Enabled: true, OnFinding: true,
		FindingMessage: vulnDispatcherFindingMessageUnrestricted,
	})
	if err != nil {
		t.Fatal(err)
	}
	customTrigger, err := pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: "vuln_dispatcher", Enabled: true, OnFinding: true,
		FindingMessage: "用户自定义触发消息：不得迁移",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pg.Exec(`DELETE FROM agent_triggers WHERE id=$1`, defaultTrigger.ID)
		_, _ = pg.Exec(`DELETE FROM agent_triggers WHERE id=$1`, customTrigger.ID)
	})

	if err := restoreDispatcherConstraintMessage(pg); err != nil {
		t.Fatal(err)
	}
	triggers, err := pg.ListTriggersFor("vuln_dispatcher")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[int64]*db.AgentTrigger, len(triggers))
	for _, trigger := range triggers {
		byID[trigger.ID] = trigger
	}
	if got := byID[defaultTrigger.ID].FindingMessage; got != vulnDispatcherFindingMessage {
		t.Fatalf("系统默认触发消息未恢复约束检查: %q", got)
	}
	if got := byID[customTrigger.ID].FindingMessage; got != customTrigger.FindingMessage {
		t.Fatalf("用户自定义触发消息被修改: %q", got)
	}
}
