// finding_migrations_test.go：Finding 编排/提示词迁移与 seed 默认行为的行为测试。
// 覆盖 plan-finding.md Verification 第 4 项：
//  1. upgradePlannerFindingDispatch 只关闭 vuln_dispatcher/trace_cleaner 旧全局
//     OnFinding 入口，其余 trigger 字段/自定义消息/其他 agent 不受影响，可重入；
//     trace_cleaner 无触发器时不 seed 不报错。
//  2. upgradeVulnDispatcherFindingPrompt 只升级旧默认 lineage，自定义 lineage 不动。
//  3. upgradeExternalFindingPrompts 升级外网内置 Planner/Worker 模板、当前提示词
//     及与旧内置正文逐字相同的任务快照；自定义模板/快照不动，可重入。
//  4. auto_default_bindings_v3 unset 时 seedOrchestrationTools 后
//     dispatch_finding_to_agent 绑定严格等于 ["planner"]，auto 不补绑；
//     新装 vuln_dispatcher trigger Enabled=true/OnFinding=false。
//  5. config-export 的 agent_triggers/agents_custom/tools_bindings 与默认行为一致。
package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"artex/agent"
	"artex/db"
)

const (
	testPlannerFindingDispatchFlag   = "planner_finding_orchestration_v1"
	testVulnDispatcherPromptGateFlag = "vuln_dispatcher_prompt_planner_gate_v1"
	testIndependentGraphFlag         = "vuln_dispatcher_independent_graph_v1"
	testExternalFindingPromptsFlag   = "external_finding_system_prompts_v4"
	testAutoDefaultBindingsFlag      = "auto_default_bindings_v3"
)

// findingMigrationServer 复用现有 NewManager harness；无 PG 配置或连不上时按现有模式 skip。
func findingMigrationServer(t *testing.T) (*Server, *Manager) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { m.Close() })
	return &Server{m: m}, m
}

// stashSettingFlag 记录 key 的当前 settings 值，返回的 cleanup 将其还原
// （原先不存在则删除）。必须在改动该 flag 之前调用。
func stashSettingFlag(t *testing.T, pg *db.DB, key string) func() {
	t.Helper()
	old, had, err := pg.GetSetting(key)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		if had {
			_ = pg.SetSetting(key, old)
		} else {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
		}
	}
}

// snapshotSettingsTable 快照整个 settings 表，返回的 cleanup 把表恢复为快照
// （删除新增 key、还原被改值）。用于 seedOrchestrationTools 全链路测试。
func snapshotSettingsTable(t *testing.T, pg *db.DB) func() {
	t.Helper()
	before := map[string]string{}
	rows, err := pg.Query(`SELECT key, value FROM settings`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		before[k] = v
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return func() {
		del, err := pg.Query(`SELECT key FROM settings`)
		if err != nil {
			return
		}
		var extra []string
		for del.Next() {
			var k string
			if err := del.Scan(&k); err == nil {
				if _, ok := before[k]; !ok {
					extra = append(extra, k)
				}
			}
		}
		_ = del.Close()
		for _, k := range extra {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, k)
		}
		for k, v := range before {
			_ = pg.SetSetting(k, v)
		}
	}
}

// snapshotToolsTable 快照 tools 表的可编辑列，cleanup 时逐行还原。
func snapshotToolsTable(t *testing.T, pg *db.DB) func() {
	t.Helper()
	type toolRow struct {
		key                 string
		description, schema string
		agents              string
		enabled             bool
	}
	before := map[string]toolRow{}
	rows, err := pg.Query(`SELECT key, description, schema::text, agents::text, enabled FROM tools`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r toolRow
		if err := rows.Scan(&r.key, &r.description, &r.schema, &r.agents, &r.enabled); err != nil {
			t.Fatal(err)
		}
		before[r.key] = r
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return func() {
		for _, r := range before {
			_, _ = pg.Exec(`UPDATE tools SET description=$2, schema=$3, agents=$4, enabled=$5 WHERE key=$1`,
				r.key, r.description, r.schema, r.agents, r.enabled)
		}
	}
}

// ensureCustomAgent 在 key 不存在时创建自定义 agent，返回是否为本次新建
// （新建的行由调用方配合 removeCustomAgent 清理）。
func ensureCustomAgent(t *testing.T, pg *db.DB, key, name, description string) bool {
	t.Helper()
	exist, err := pg.GetAgentByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if exist != nil {
		return false
	}
	if _, err := pg.CreateAgent(key, name, description); err != nil {
		t.Fatal(err)
	}
	return true
}

func removeCustomAgent(t *testing.T, pg *db.DB, key string, created bool) {
	t.Helper()
	if !created {
		return
	}
	_ = pg.DeleteTriggersForAgent(key)
	if err := pg.DeleteAgent(key); err != nil {
		t.Fatal(err)
	}
}

// replaceTriggers 把 agentKey 的全部触发器替换为 fixtures，返回的 cleanup
// 删除 fixtures 并按原字段重建旧触发器（触发器 ID 会变化，last_fire 不保留；
// 只用于测试夹具恢复，不影响被测行为）。
func replaceTriggers(t *testing.T, pg *db.DB, agentKey string, fixtures ...*db.AgentTrigger) func() {
	t.Helper()
	saved, err := pg.ListTriggersFor(agentKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, agentKey); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		if _, err := pg.CreateTrigger(f); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		_, _ = pg.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, agentKey)
		for _, tr := range saved {
			tr.ID = 0
			_, _ = pg.CreateTrigger(tr)
		}
	}
}

// fingerprintTriggers 把所有给定 agent 的触发器全部字段序列化成字符串，
// 用于重入/无副作用断言（ID 稳定时包含 ID）。
func fingerprintTriggers(t *testing.T, pg *db.DB, keys ...string) string {
	t.Helper()
	var b strings.Builder
	for _, k := range keys {
		trs, err := pg.ListTriggersFor(k)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range trs {
			fmt.Fprintf(&b, "%s|%d|%v|%d|%v|%v|%v|%v|%v|%q|%q|%q|%q|%q|%q|%v\n",
				k, tr.ID, tr.Enabled, tr.IntervalSec, tr.OnFinding, tr.OnGoalMet,
				tr.OnTaskTimeout, tr.OnToolCall, tr.OnTaskCreate,
				tr.IntervalMessage, tr.FindingMessage, tr.GoalMessage,
				tr.TaskTimeoutMessage, tr.ToolCallMessage, tr.TaskCreateMessage, tr.ToolNames)
		}
	}
	return b.String()
}

// promptState 捕获一个 agent 的提示词版本状态，支持精确恢复
// （删除新增版本行 + 指回旧 current_prompt_id）。
type promptState struct {
	agentID     int64
	currentID   sql.NullInt64
	maxPromptID int64
	count       int
}

func capturePromptState(t *testing.T, pg *db.DB, agentID int64) *promptState {
	t.Helper()
	st := &promptState{agentID: agentID}
	if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, agentID).Scan(&st.currentID); err != nil {
		t.Fatal(err)
	}
	if err := pg.QueryRow(`SELECT count(*), COALESCE(max(id),0) FROM agent_prompts WHERE agent_id=$1`, agentID).
		Scan(&st.count, &st.maxPromptID); err != nil {
		t.Fatal(err)
	}
	return st
}

func (st *promptState) restore(pg *db.DB) {
	_, _ = pg.Exec(`DELETE FROM agent_prompts WHERE agent_id=$1 AND id>$2`, st.agentID, st.maxPromptID)
	var cur any
	if st.currentID.Valid {
		cur = st.currentID.Int64
	}
	_, _ = pg.Exec(`UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, cur, st.agentID)
}

// TestPlannerFindingDispatchMigration 验证 upgradePlannerFindingDispatch：
// 旧 V2 dispatcher message 升级为新显式派发消息、两个目标 agent 的每个现存
// trigger OnFinding 置 false；Enabled、interval、其他 trigger bool、run policy
// 字段与自定义消息不变；其他自定义 agent 的 OnFinding 不受影响；重入无副作用。
func TestPlannerFindingDispatchMigration(t *testing.T) {
	s, m := findingMigrationServer(t)
	pg := m.pg
	t.Cleanup(stashSettingFlag(t, pg, testPlannerFindingDispatchFlag))
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testPlannerFindingDispatchFlag); err != nil {
		t.Fatal(err)
	}

	createdDisp := ensureCustomAgent(t, pg, "vuln_dispatcher", "漏洞派发", vulnDispatcherDescriptionV1)
	t.Cleanup(func() { removeCustomAgent(t, pg, "vuln_dispatcher", createdDisp) })
	disp, err := pg.GetAgentByKey("vuln_dispatcher")
	if err != nil {
		t.Fatal(err)
	}
	origDesc := disp.Description
	t.Cleanup(func() {
		_, _ = pg.Exec(`UPDATE agents SET description=NULLIF($1,'') WHERE id=$2`, origDesc, disp.ID)
	})

	// 对照组：普通自定义 agent 的全局 OnFinding 不应被迁移关掉。
	stamp := time.Now().UnixNano()
	ctlKey := fmt.Sprintf("t_finding_ctl_%d", stamp)
	if _, err := pg.CreateAgent(ctlKey, "迁移对照", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pg.DeleteTriggersForAgent(ctlKey)
		removeCustomAgent(t, pg, ctlKey, true)
	})
	ctlTrigger, err := pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: ctlKey, Enabled: true, OnFinding: true, FindingMessage: "对照触发消息",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(replaceTriggers(t, pg, "vuln_dispatcher",
		&db.AgentTrigger{
			AgentKey: "vuln_dispatcher", Enabled: true, OnFinding: true,
			FindingMessage: vulnDispatcherFindingMessageV2,
			IntervalSec:    300, IntervalMessage: "每 5 分钟巡检",
			OnGoalMet: true, GoalMessage: "目标达成后复核",
		},
		&db.AgentTrigger{
			AgentKey: "vuln_dispatcher", Enabled: false, OnFinding: true,
			FindingMessage: "用户自定义 finding 消息",
		},
	))
	t.Cleanup(replaceTriggers(t, pg, "trace_cleaner",
		&db.AgentTrigger{
			AgentKey: "trace_cleaner", Enabled: true, OnFinding: true,
			FindingMessage:     "清理触发消息",
			OnTaskTimeout:      true,
			TaskTimeoutMessage: "超时后补清理",
			ToolNames:          []string{"record_fact"},
		},
	))

	if _, err := pg.Exec(`UPDATE agents SET description=$1 WHERE id=$2`, vulnDispatcherDescriptionV1, disp.ID); err != nil {
		t.Fatal(err)
	}

	s.upgradePlannerFindingDispatch()

	if v, _, _ := pg.GetSetting(testPlannerFindingDispatchFlag); v != "true" {
		t.Fatalf("迁移完成后 flag 未写入: %q", v)
	}
	disp, _ = pg.GetAgentByKey("vuln_dispatcher")
	if disp.Description != vulnDispatcherDescription {
		t.Fatalf("dispatcher 描述未从 V1 升级: %q", disp.Description)
	}

	dispTrs, err := pg.ListTriggersFor("vuln_dispatcher")
	if err != nil || len(dispTrs) != 2 {
		t.Fatalf("dispatcher 触发器数量异常: %v %v", dispTrs, err)
	}
	legacy, custom := dispTrs[0], dispTrs[1]
	if legacy.OnFinding {
		t.Fatal("旧 V2 消息触发器 OnFinding 应为 false")
	}
	if legacy.FindingMessage != vulnDispatcherFindingMessage {
		t.Fatal("旧 V2 消息未升级为新显式派发消息")
	}
	if !legacy.Enabled || legacy.IntervalSec != 300 || legacy.IntervalMessage != "每 5 分钟巡检" ||
		!legacy.OnGoalMet || legacy.GoalMessage != "目标达成后复核" {
		t.Fatalf("旧触发器 Enabled/interval/其他 bool/消息被改动: %+v", legacy)
	}
	if custom.FindingMessage != "用户自定义 finding 消息" {
		t.Fatal("自定义 finding 消息被迁移改写")
	}
	if custom.OnFinding != false {
		t.Fatal("自定义消息触发器 OnFinding 也应关闭")
	}
	if custom.Enabled {
		t.Fatal("自定义触发器 Enabled 原样为 false，不应被改动")
	}

	cleanerTrs, err := pg.ListTriggersFor("trace_cleaner")
	if err != nil || len(cleanerTrs) != 1 {
		t.Fatalf("trace_cleaner 触发器数量异常: %v %v", cleanerTrs, err)
	}
	ct := cleanerTrs[0]
	if ct.OnFinding || !ct.Enabled || ct.FindingMessage != "清理触发消息" ||
		!ct.OnTaskTimeout || ct.TaskTimeoutMessage != "超时后补清理" ||
		len(ct.ToolNames) != 1 || ct.ToolNames[0] != "record_fact" {
		t.Fatalf("trace_cleaner 触发器迁移不符合只关 OnFinding 的约定: %+v", ct)
	}

	ctlTrs, err := pg.ListTriggersFor(ctlKey)
	if err != nil || len(ctlTrs) != 1 {
		t.Fatalf("对照 agent 触发器异常: %v %v", ctlTrs, err)
	}
	if ctlTrs[0].ID != ctlTrigger.ID || !ctlTrs[0].OnFinding {
		t.Fatal("其他自定义 agent 的 OnFinding 必须保持 true")
	}

	// 重入无副作用：flag 已置位，再跑一次任何行都不变。
	fpBefore := fingerprintTriggers(t, pg, "vuln_dispatcher", "trace_cleaner", ctlKey)
	s.upgradePlannerFindingDispatch()
	if fp := fingerprintTriggers(t, pg, "vuln_dispatcher", "trace_cleaner", ctlKey); fp != fpBefore {
		t.Fatalf("重入产生副作用:\nbefore=%s\nafter=%s", fpBefore, fp)
	}

	// 自定义描述不被改写：人为改成用户描述、清 flag 再跑，描述与触发器都不动。
	if _, err := pg.Exec(`UPDATE agents SET description='用户自定义描述' WHERE id=$1`, disp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testPlannerFindingDispatchFlag); err != nil {
		t.Fatal(err)
	}
	s.upgradePlannerFindingDispatch()
	disp, _ = pg.GetAgentByKey("vuln_dispatcher")
	if disp.Description != "用户自定义描述" {
		t.Fatal("自定义描述被迁移覆盖")
	}
	if fp := fingerprintTriggers(t, pg, "vuln_dispatcher", "trace_cleaner", ctlKey); fp != fpBefore {
		t.Fatalf("重入(自定义描述)产生副作用:\nbefore=%s\nafter=%s", fpBefore, fp)
	}
}

// TestPlannerFindingDispatchWithoutTraceCleaner 验证 trace_cleaner 没有任何
// trigger（乃至 agent）时迁移照常完成：不 seed 新触发器、不报错。
func TestPlannerFindingDispatchWithoutTraceCleaner(t *testing.T) {
	s, m := findingMigrationServer(t)
	pg := m.pg
	t.Cleanup(stashSettingFlag(t, pg, testPlannerFindingDispatchFlag))
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testPlannerFindingDispatchFlag); err != nil {
		t.Fatal(err)
	}

	createdDisp := ensureCustomAgent(t, pg, "vuln_dispatcher", "漏洞派发", "")
	t.Cleanup(func() { removeCustomAgent(t, pg, "vuln_dispatcher", createdDisp) })

	t.Cleanup(replaceTriggers(t, pg, "vuln_dispatcher",
		&db.AgentTrigger{
			AgentKey: "vuln_dispatcher", Enabled: true, OnFinding: true,
			FindingMessage: vulnDispatcherFindingMessageV2,
		},
	))
	// trace_cleaner 完全没有触发器（agent 也不创建）。
	t.Cleanup(replaceTriggers(t, pg, "trace_cleaner"))

	s.upgradePlannerFindingDispatch()

	if v, _, _ := pg.GetSetting(testPlannerFindingDispatchFlag); v != "true" {
		t.Fatalf("trace_cleaner 缺席时迁移也应完成并写 flag: %q", v)
	}
	if trs, err := pg.ListTriggersFor("trace_cleaner"); err != nil || len(trs) != 0 {
		t.Fatalf("trace_cleaner 不存在的场景不得 seed 触发器: %v %v", trs, err)
	}
	dispTrs, err := pg.ListTriggersFor("vuln_dispatcher")
	if err != nil || len(dispTrs) != 1 || dispTrs[0].OnFinding {
		t.Fatalf("dispatcher 触发器仍应完成迁移: %v %v", dispTrs, err)
	}
}

// TestVulnDispatcherFindingPromptMigration 验证 lineage 守卫的提示词迁移：
// 旧默认血统升级为新 Planner 派发默认提示词；自定义血统不动；重入无副作用。
func TestVulnDispatcherFindingPromptMigration(t *testing.T) {
	s, m := findingMigrationServer(t)
	pg := m.pg
	t.Cleanup(stashSettingFlag(t, pg, testVulnDispatcherPromptGateFlag))
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testVulnDispatcherPromptGateFlag); err != nil {
		t.Fatal(err)
	}

	created := ensureCustomAgent(t, pg, "vuln_dispatcher", "漏洞派发", "")
	t.Cleanup(func() { removeCustomAgent(t, pg, "vuln_dispatcher", created) })
	disp, err := pg.GetAgentByKey("vuln_dispatcher")
	if err != nil {
		t.Fatal(err)
	}
	st := capturePromptState(t, pg, disp.ID)
	t.Cleanup(func() { st.restore(pg) })

	wantDefault := s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt)

	// 旧默认血统：三个特征句都在、且不含新派发入口标记。
	oldLineage := "漏洞派发 agent（旧默认血统）。审核开关由人决定。去重的键是资产。" +
		"自动 finding 触发时按 vulnclass 与 severity 判定是否派发。"
	if _, err := pg.SavePrompt(disp.ID, oldLineage, "测试夹具", "test"); err != nil {
		t.Fatal(err)
	}
	s.upgradeVulnDispatcherFindingPrompt()
	if v, _, _ := pg.GetSetting(testVulnDispatcherPromptGateFlag); v != "true" {
		t.Fatalf("提示词迁移完成后 flag 未写入: %q", v)
	}
	cur, err := pg.CurrentPrompt(disp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur != wantDefault {
		t.Fatal("旧默认血统未被升级为新 Planner 派发默认提示词")
	}

	// 自定义血统：不含默认特征句，任何次数的迁移都不动它。
	custom := "完全自定义的派发提示词：只认人工提交的 finding 清单。"
	if _, err := pg.SavePrompt(disp.ID, custom, "用户自定义", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testVulnDispatcherPromptGateFlag); err != nil {
		t.Fatal(err)
	}
	s.upgradeVulnDispatcherFindingPrompt()
	if cur, _ := pg.CurrentPrompt(disp.ID); cur != custom {
		t.Fatal("自定义 lineage 被迁移覆盖")
	}
	if v, _, _ := pg.GetSetting(testVulnDispatcherPromptGateFlag); v != "true" {
		t.Fatal("自定义血统场景迁移也应写 flag（否则每次启动重复扫描）")
	}

	// 已是新默认血统：恢复新默认后清 flag 再跑，不得重复 Reset。
	if _, err := pg.SavePrompt(disp.ID, wantDefault, "恢复新默认夹具", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testVulnDispatcherPromptGateFlag); err != nil {
		t.Fatal(err)
	}
	before := capturePromptState(t, pg, disp.ID)
	s.upgradeVulnDispatcherFindingPrompt()
	after := capturePromptState(t, pg, disp.ID)
	if after.count != before.count {
		t.Fatalf("新默认血统被重复 Reset: before=%d after=%d", before.count, after.count)
	}
	if cur, _ := pg.CurrentPrompt(disp.ID); cur != wantDefault {
		t.Fatal("重入改写了已是新默认的提示词")
	}
}

func TestUpgradeVulnDispatcherIndependentGraph(t *testing.T) {
	s, m := findingMigrationServer(t)
	pg := m.pg
	t.Cleanup(stashSettingFlag(t, pg, testIndependentGraphFlag))
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testIndependentGraphFlag); err != nil {
		t.Fatal(err)
	}

	created := ensureCustomAgent(t, pg, "vuln_dispatcher", "漏洞派发", "")
	t.Cleanup(func() { removeCustomAgent(t, pg, "vuln_dispatcher", created) })
	disp, err := pg.GetAgentByKey("vuln_dispatcher")
	if err != nil {
		t.Fatal(err)
	}
	st := capturePromptState(t, pg, disp.ID)
	t.Cleanup(func() { st.restore(pg) })

	oldDefault := "漏洞派发 agent。审核开关由人决定。去重的键是实际资产。" +
		"parent_ref = 触发任务 id；source_task_ids = [触发任务 id]。"
	if _, err := pg.SavePrompt(disp.ID, oldDefault, "测试夹具", "test"); err != nil {
		t.Fatal(err)
	}
	s.upgradeVulnDispatcherIndependentGraph()
	wantDefault := s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt)
	current, err := pg.CurrentPrompt(disp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current != wantDefault || strings.Contains(current, "source_task_ids = [触发任务 id]") {
		t.Fatal("旧默认血统未切换到独立探索链提示词")
	}
	if value, _, _ := pg.GetSetting(testIndependentGraphFlag); value != "true" {
		t.Fatalf("迁移完成后 flag 未写入: %q", value)
	}
	before := capturePromptState(t, pg, disp.ID)
	s.upgradeVulnDispatcherIndependentGraph()
	if after := capturePromptState(t, pg, disp.ID); after.count != before.count {
		t.Fatalf("重入新增了提示词版本: before=%d after=%d", before.count, after.count)
	}

	custom := "完全自定义派发规则；仍保留 source_task_ids = [触发任务 id]。"
	if _, err := pg.SavePrompt(disp.ID, custom, "用户自定义", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testIndependentGraphFlag); err != nil {
		t.Fatal(err)
	}
	customBefore := capturePromptState(t, pg, disp.ID)
	s.upgradeVulnDispatcherIndependentGraph()
	customAfter := capturePromptState(t, pg, disp.ID)
	current, err = pg.CurrentPrompt(disp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current != custom || customAfter.currentID != customBefore.currentID || customAfter.count != customBefore.count {
		t.Fatal("自定义提示词血统被独立探索链迁移覆盖")
	}
}

// TestExternalFindingPromptsMigration 验证外网内置 Planner/Worker 提示词迁移：
// 内置模板行、planner/worker 当前提示词、与旧内置正文逐字相同的任务快照升级；
// 自定义模板与自定义快照不动；重入无副作用。
func TestExternalFindingPromptsMigration(t *testing.T) {
	s, m := findingMigrationServer(t)
	pg := m.pg
	t.Cleanup(stashSettingFlag(t, pg, testExternalFindingPromptsFlag))
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testExternalFindingPromptsFlag); err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	oldPlannerBody := fmt.Sprintf("【Finding迁移测试】旧版外网渗透 Planner 内置正文 v0（stamp=%d）。", stamp)
	oldWorkerBody := fmt.Sprintf("【Finding迁移测试】旧版外网渗透 Worker 内置正文 v0（stamp=%d）。", stamp)
	customPlannerBody := "自定义 planner 模板正文（迁移不得修改）。"
	customWorkerBody := "自定义 worker 模板正文（迁移不得修改）。"
	customPlannerSnap := "自定义 planner 任务快照（迁移不得修改）。"
	customWorkerSnap := "自定义 worker 任务快照（迁移不得修改）。"
	seeds := agent.BuiltinPromptSeeds()

	// ── 内置 Planner 模板行：强制为旧正文（builtin 必须为 true 才会被迁移命中）──
	plannerTpl, err := pg.GetPlannerTemplateByNKey(db.ExternalPentestTemplateName)
	if err != nil {
		t.Fatal(err)
	}
	plannerCreated := false
	if plannerTpl == nil {
		if _, err := pg.SeedPlannerTemplateIfEmpty(db.PlannerTemplateBuiltinName, db.PlannerTemplateBuiltinNKey, oldPlannerBody, true); err != nil {
			t.Fatal(err)
		}
		plannerTpl, err = pg.GetPlannerTemplateByNKey(db.ExternalPentestTemplateName)
		if err != nil || plannerTpl == nil {
			t.Fatalf("内置 planner 模板 seed 失败: %v", err)
		}
		plannerCreated = true
	}
	if plannerCreated {
		t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM planner_templates WHERE id=$1`, plannerTpl.ID) })
	} else {
		origBody, origBuiltin, origDefault := plannerTpl.Body, plannerTpl.Builtin, plannerTpl.IsDefault
		t.Cleanup(func() {
			_, _ = pg.Exec(`UPDATE planner_templates SET body=$2, builtin=$3, is_default=$4 WHERE id=$1`,
				plannerTpl.ID, origBody, origBuiltin, origDefault)
		})
	}
	if _, err := pg.Exec(`UPDATE planner_templates SET body=$1, builtin=true WHERE id=$2`, oldPlannerBody, plannerTpl.ID); err != nil {
		t.Fatal(err)
	}

	// ── 内置 Worker 模板行 ──
	workerTpl, err := pg.GetWorkerTemplateByNKey(db.ExternalPentestTemplateName)
	if err != nil {
		t.Fatal(err)
	}
	workerCreated := false
	if workerTpl == nil {
		if _, err := pg.SeedWorkerTemplateIfEmpty(db.WorkerTemplateBuiltinName, db.WorkerTemplateBuiltinNKey, oldWorkerBody, true); err != nil {
			t.Fatal(err)
		}
		workerTpl, err = pg.GetWorkerTemplateByNKey(db.ExternalPentestTemplateName)
		if err != nil || workerTpl == nil {
			t.Fatalf("内置 worker 模板 seed 失败: %v", err)
		}
		workerCreated = true
	}
	if workerCreated {
		t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM worker_templates WHERE id=$1`, workerTpl.ID) })
	} else {
		origBody, origBuiltin, origDefault := workerTpl.Body, workerTpl.Builtin, workerTpl.IsDefault
		t.Cleanup(func() {
			_, _ = pg.Exec(`UPDATE worker_templates SET body=$2, builtin=$3, is_default=$4 WHERE id=$1`,
				workerTpl.ID, origBody, origBuiltin, origDefault)
		})
	}
	if _, err := pg.Exec(`UPDATE worker_templates SET body=$1, builtin=true WHERE id=$2`, oldWorkerBody, workerTpl.ID); err != nil {
		t.Fatal(err)
	}

	// ── 自定义模板：迁移不得触碰 ──
	customPlannerTpl, err := pg.CreatePlannerTemplate(db.PlannerTemplateInput{
		Name: fmt.Sprintf("t-mig-custom-planner-%d", stamp), Body: customPlannerBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM planner_templates WHERE id=$1`, customPlannerTpl.ID) })
	customWorkerTpl, err := pg.CreateWorkerTemplate(db.WorkerTemplateInput{
		Name: fmt.Sprintf("t-mig-custom-worker-%d", stamp), Body: customWorkerBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM worker_templates WHERE id=$1`, customWorkerTpl.ID) })

	// ── planner/worker 当前提示词：强制为旧正文 ──
	plannerAgent, err := pg.GetAgentByKey("planner")
	if err != nil || plannerAgent == nil {
		t.Fatalf("内置 planner agent 缺失: %v", err)
	}
	workerAgent, err := pg.GetAgentByKey("worker")
	if err != nil || workerAgent == nil {
		t.Fatalf("内置 worker agent 缺失: %v", err)
	}
	plannerPrompt := capturePromptState(t, pg, plannerAgent.ID)
	t.Cleanup(func() { plannerPrompt.restore(pg) })
	workerPrompt := capturePromptState(t, pg, workerAgent.ID)
	t.Cleanup(func() { workerPrompt.restore(pg) })
	if _, err := pg.SavePrompt(plannerAgent.ID, oldPlannerBody, "测试夹具", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.SavePrompt(workerAgent.ID, oldWorkerBody, "测试夹具", "test"); err != nil {
		t.Fatal(err)
	}
	plannerBeforeMigration := capturePromptState(t, pg, plannerAgent.ID).count
	workerBeforeMigration := capturePromptState(t, pg, workerAgent.ID).count

	// ── 任务快照：一条逐字等于旧内置正文（默认快照），一条自定义 ──
	taskDefault, err := m.CreateTaskWithOptions(fmt.Sprintf("t-finding-mig-default-%d", stamp), "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	taskCustom, err := m.CreateTaskWithOptions(fmt.Sprintf("t-finding-mig-custom-%d", stamp), "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defaultTaskID := mustTaskID(t, taskDefault.ID)
	customTaskID := mustTaskID(t, taskCustom.ID)
	t.Cleanup(func() {
		_, _ = pg.Exec(`DELETE FROM tasks WHERE id IN ($1,$2)`, defaultTaskID, customTaskID)
		_, _ = pg.Exec(`DELETE FROM explorations WHERE id IN ($1,$2)`, taskDefault.ExpID, taskCustom.ExpID)
	})
	if _, err := pg.Exec(`UPDATE tasks SET planner_prompt_override=$1, worker_prompt_override=$2 WHERE id=$3`,
		oldPlannerBody, oldWorkerBody, defaultTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`UPDATE tasks SET planner_prompt_override=$1, worker_prompt_override=$2 WHERE id=$3`,
		customPlannerSnap, customWorkerSnap, customTaskID); err != nil {
		t.Fatal(err)
	}

	s.upgradeExternalFindingPrompts()

	if v, _, _ := pg.GetSetting(testExternalFindingPromptsFlag); v != "true" {
		t.Fatalf("迁移完成后 flag 未写入: %q", v)
	}
	gotPlannerTpl, err := pg.GetPlannerTemplateByNKey(db.ExternalPentestTemplateName)
	if err != nil {
		t.Fatal(err)
	}
	if gotPlannerTpl.Body != seeds["planner"] {
		t.Fatal("内置 Planner 模板行未升级为新正文")
	}
	gotWorkerTpl, err := pg.GetWorkerTemplateByNKey(db.ExternalPentestTemplateName)
	if err != nil {
		t.Fatal(err)
	}
	if gotWorkerTpl.Body != seeds["worker"] {
		t.Fatal("内置 Worker 模板行未升级为新正文")
	}
	if got, _ := pg.CurrentPrompt(plannerAgent.ID); got != seeds["planner"] {
		t.Fatal("planner 当前提示词未升级为新正文")
	}
	if got, _ := pg.CurrentPrompt(workerAgent.ID); got != seeds["worker"] {
		t.Fatal("worker 当前提示词未升级为新正文")
	}
	var snapPlanner, snapWorker string
	if err := pg.QueryRow(`SELECT planner_prompt_override, worker_prompt_override FROM tasks WHERE id=$1`, defaultTaskID).
		Scan(&snapPlanner, &snapWorker); err != nil {
		t.Fatal(err)
	}
	if snapPlanner != seeds["planner"] || snapWorker != seeds["worker"] {
		t.Fatal("与旧内置正文逐字相同的默认任务快照未升级")
	}
	if err := pg.QueryRow(`SELECT planner_prompt_override, worker_prompt_override FROM tasks WHERE id=$1`, customTaskID).
		Scan(&snapPlanner, &snapWorker); err != nil {
		t.Fatal(err)
	}
	if snapPlanner != customPlannerSnap || snapWorker != customWorkerSnap {
		t.Fatal("自定义任务快照被迁移修改")
	}
	if cp, err := pg.GetPlannerTemplate(customPlannerTpl.ID); err != nil || cp == nil || cp.Body != customPlannerBody {
		t.Fatalf("自定义 Planner 模板被迁移修改: %v", err)
	}
	if cw, err := pg.GetWorkerTemplate(customWorkerTpl.ID); err != nil || cw == nil || cw.Body != customWorkerBody {
		t.Fatalf("自定义 Worker 模板被迁移修改: %v", err)
	}
	if n := capturePromptState(t, pg, plannerAgent.ID).count; n != plannerBeforeMigration+1 {
		t.Fatalf("planner 提示词版本应恰好新增一版: before=%d after=%d", plannerBeforeMigration, n)
	}
	if n := capturePromptState(t, pg, workerAgent.ID).count; n != workerBeforeMigration+1 {
		t.Fatalf("worker 提示词版本应恰好新增一版: before=%d after=%d", workerBeforeMigration, n)
	}

	// 重入无副作用：清 flag 让 applyExternalFindingPrompts 真正再跑一遍（不是
	// 只吃 flag 早退），模板/快照/自定义行/提示词版本数全部不变。
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testExternalFindingPromptsFlag); err != nil {
		t.Fatal(err)
	}
	var dP, dW string
	if err := pg.QueryRow(`SELECT planner_prompt_override, worker_prompt_override FROM tasks WHERE id=$1`, defaultTaskID).
		Scan(&dP, &dW); err != nil {
		t.Fatal(err)
	}
	fpBefore := fmt.Sprintf("tpl:%s|%s|default-snap:%s,%s|custom-snap:%s,%s|",
		gotPlannerTpl.Body, gotWorkerTpl.Body, dP, dW, snapPlanner, snapWorker)
	verPlanner := capturePromptState(t, pg, plannerAgent.ID).count
	verWorker := capturePromptState(t, pg, workerAgent.ID).count

	s.upgradeExternalFindingPrompts()

	gotPlannerTpl, _ = pg.GetPlannerTemplateByNKey(db.ExternalPentestTemplateName)
	gotWorkerTpl, _ = pg.GetWorkerTemplateByNKey(db.ExternalPentestTemplateName)
	var dP2, dW2, cP2, cW2 string
	if err := pg.QueryRow(`SELECT planner_prompt_override, worker_prompt_override FROM tasks WHERE id=$1`, defaultTaskID).
		Scan(&dP2, &dW2); err != nil {
		t.Fatal(err)
	}
	if err := pg.QueryRow(`SELECT planner_prompt_override, worker_prompt_override FROM tasks WHERE id=$1`, customTaskID).
		Scan(&cP2, &cW2); err != nil {
		t.Fatal(err)
	}
	fpAfter := fmt.Sprintf("tpl:%s|%s|default-snap:%s,%s|custom-snap:%s,%s|",
		gotPlannerTpl.Body, gotWorkerTpl.Body, dP2, dW2, cP2, cW2)
	if fpAfter != fpBefore {
		t.Fatalf("重入产生副作用:\nbefore=%s\nafter=%s", fpBefore, fpAfter)
	}
	if n := capturePromptState(t, pg, plannerAgent.ID).count; n != verPlanner {
		t.Fatalf("重入给 planner 追加了提示词版本: %d→%d", verPlanner, n)
	}
	if n := capturePromptState(t, pg, workerAgent.ID).count; n != verWorker {
		t.Fatalf("重入给 worker 追加了提示词版本: %d→%d", verWorker, n)
	}
	if cp, err := pg.GetPlannerTemplate(customPlannerTpl.ID); err != nil || cp == nil || cp.Body != customPlannerBody {
		t.Fatalf("重入修改了自定义 Planner 模板: %v", err)
	}
	if cw, err := pg.GetWorkerTemplate(customWorkerTpl.ID); err != nil || cw == nil || cw.Body != customWorkerBody {
		t.Fatalf("重入修改了自定义 Worker 模板: %v", err)
	}
}

// TestSeedOrchestrationToolsFindingDispatchBinding 验证 auto_default_bindings_v3
// unset 时执行完整 seedOrchestrationTools：dispatch_finding_to_agent 绑定严格等于
// ["planner"]（seedAutoDefaultBindings 不得把 auto 补绑回来）；新装的
// vuln_dispatcher trigger 为 Enabled=true/OnFinding=false。
func TestSeedOrchestrationToolsFindingDispatchBinding(t *testing.T) {
	s, m := findingMigrationServer(t)
	pg := m.pg

	t.Cleanup(snapshotSettingsTable(t, pg))
	t.Cleanup(snapshotToolsTable(t, pg))
	t.Cleanup(stashSettingFlag(t, pg, testAutoDefaultBindingsFlag))

	// 模拟旧安装：dispatch_finding_to_agent 工具行由 SeedTool 以代码默认重建，
	// 而不是沿用库里可能已被改过的绑定。
	if _, err := pg.Exec(`DELETE FROM tools WHERE key='dispatch_finding_to_agent'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, testAutoDefaultBindingsFlag); err != nil {
		t.Fatal(err)
	}
	dispBefore, err := pg.GetAgentByKey("vuln_dispatcher")
	if err != nil {
		t.Fatal(err)
	}
	freshInstall := dispBefore == nil
	// 本次 seed 链是否会实际执行 upgradePlannerFindingDispatch（flag 未置位才会跑）。
	_, dispatchFlagWasSet, err := pg.GetSetting(testPlannerFindingDispatchFlag)
	if err != nil {
		t.Fatal(err)
	}

	s.seedOrchestrationTools()

	if v, _, _ := pg.GetSetting(testAutoDefaultBindingsFlag); v != "true" {
		t.Fatalf("seedAutoDefaultBindings 应执行并写 flag: %q", v)
	}
	tool, err := pg.GetTool("dispatch_finding_to_agent")
	if err != nil || tool == nil {
		t.Fatalf("dispatch_finding_to_agent 工具行缺失: %v", err)
	}
	if len(tool.Agents) != 1 || tool.Agents[0] != "planner" {
		t.Fatalf("dispatch_finding_to_agent 绑定必须严格等于 [planner], 实际 %v", tool.Agents)
	}
	for _, a := range tool.Agents {
		if a == "auto" {
			t.Fatal("seedAutoDefaultBindings 不得把 auto 补绑到 dispatch_finding_to_agent")
		}
	}

	disp, err := pg.GetAgentByKey("vuln_dispatcher")
	if err != nil || disp == nil {
		t.Fatalf("seed 链后 vuln_dispatcher agent 缺失: %v", err)
	}
	trs, err := pg.ListTriggersFor("vuln_dispatcher")
	if err != nil || len(trs) == 0 {
		t.Fatalf("seed 链后 vuln_dispatcher 触发器缺失: %v %v", trs, err)
	}
	if !dispatchFlagWasSet {
		// 本次链实际跑了编排迁移：所有 dispatcher 触发器必须已关闭旧 OnFinding 入口。
		for _, tr := range trs {
			if tr.OnFinding {
				t.Fatalf("seed 链完成后仍存在 OnFinding=true 的 dispatcher 触发器: %+v", tr)
			}
		}
	}
	if freshInstall {
		if disp.Description != vulnDispatcherDescription {
			t.Fatalf("新装 dispatcher 描述不符合 planner 显式派发入口: %q", disp.Description)
		}
		found := false
		for _, tr := range trs {
			if tr.Enabled && !tr.OnFinding && tr.FindingMessage == vulnDispatcherFindingMessage {
				found = true
			}
		}
		if !found {
			t.Fatalf("新装 dispatcher 触发器应为 Enabled=true/OnFinding=false/新派发消息: %+v", trs[0])
		}
	}
}

// TestFindingMigrationConfigExportConsistency 验证 config-export 与迁移后的
// 默认行为一致。文件是 COPY text 双层转义，沿用项目既有 readExportLines 解码；
// 只断言能捕捉配置回归的语义字段，不 pin 源文本字节。
func TestFindingMigrationConfigExportConsistency(t *testing.T) {
	type triggerRow struct {
		AgentKey       string `json:"agent_key"`
		Enabled        bool   `json:"enabled"`
		OnFinding      bool   `json:"on_finding"`
		FindingMessage string `json:"finding_message"`
	}
	found := map[string]triggerRow{}
	for _, line := range readExportLines(t, "../config-export/agent_triggers.jsonl") {
		var row triggerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("agent_triggers.jsonl 解码失败（COPY 双层转义处理错误？）: %v", err)
		}
		found[row.AgentKey] = row
	}
	disp, ok := found["vuln_dispatcher"]
	if !ok {
		t.Fatal("config-export 缺少 vuln_dispatcher 触发器行")
	}
	if !disp.Enabled || disp.OnFinding {
		t.Fatalf("config-export dispatcher 触发器必须为 enabled=true/on_finding=false: %+v", disp)
	}
	if disp.FindingMessage != vulnDispatcherFindingMessage {
		t.Fatal("config-export dispatcher finding_message 与代码默认派发消息不一致")
	}
	cleaner, ok := found["trace_cleaner"]
	if !ok {
		t.Fatal("config-export 缺少 trace_cleaner 触发器行")
	}
	if !cleaner.Enabled || cleaner.OnFinding {
		t.Fatalf("config-export trace_cleaner 触发器必须为 enabled=true/on_finding=false: %+v", cleaner)
	}
	if strings.TrimSpace(cleaner.FindingMessage) == "" {
		t.Fatal("trace_cleaner 触发器应保留原 finding 消息作为显式派发消息模板")
	}

	type bindingRow struct {
		Key     string   `json:"key"`
		Agents  []string `json:"agents"`
		Enabled bool     `json:"enabled"`
	}
	bindingFound := false
	for _, line := range readExportLines(t, "../config-export/tools_bindings.jsonl") {
		var row bindingRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("tools_bindings.jsonl 解码失败: %v", err)
		}
		if row.Key != "dispatch_finding_to_agent" {
			continue
		}
		bindingFound = true
		if !row.Enabled {
			t.Fatal("config-export dispatch_finding_to_agent 必须为 enabled")
		}
		if len(row.Agents) != 1 || row.Agents[0] != "planner" {
			t.Fatalf("config-export dispatch_finding_to_agent 绑定必须严格等于 [planner], 实际 %v", row.Agents)
		}
	}
	if !bindingFound {
		t.Fatal("config-export 缺少 dispatch_finding_to_agent 绑定行")
	}

	type agentRow struct {
		Key         string `json:"key"`
		Description string `json:"description"`
	}
	customFound := map[string]agentRow{}
	for _, line := range readExportLines(t, "../config-export/agents_custom.jsonl") {
		var row agentRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("agents_custom.jsonl 解码失败: %v", err)
		}
		customFound[row.Key] = row
	}
	exportDisp, ok := customFound["vuln_dispatcher"]
	if !ok {
		t.Fatal("config-export 缺少 vuln_dispatcher 自定义 agent 行")
	}
	if exportDisp.Description != vulnDispatcherDescription {
		t.Fatal("config-export dispatcher 入口描述与 planner 显式派发默认描述不一致")
	}
	exportCleaner, ok := customFound["trace_cleaner"]
	if !ok {
		t.Fatal("config-export 缺少 trace_cleaner 自定义 agent 行")
	}
	if !strings.Contains(exportCleaner.Description, "显式派发") {
		t.Fatalf("config-export trace_cleaner 入口描述应说明 planner 显式派发: %q", exportCleaner.Description)
	}
}
