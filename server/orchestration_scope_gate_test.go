package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"artex/agent"
	db "artex/db"
)

// TestSpawnTaskScopeGates 验证 spawn_task 的两道代码侧硬闸门：
//   - 父任务 goal 中的「禁止X」行命中子任务 goal → 拒绝，且不创建任务；
//   - 父任务 task_constraints 的 deny「禁止X」命中 → 拒绝，且不创建任务；
//   - 无匹配方向不被额外拦截，正常创建子任务。
func TestSpawnTaskScopeGatesRejectAndAllow(t *testing.T) {
	m, s := fiFixture(t)
	ensureScopeGateSpawnTemplates(t, m.pg)
	stamp := time.Now().UnixNano()

	goalParent, err := m.CreateTaskWithOptions("scope gate goal parent", "授权任务\n禁止内网渗透\n仅测试明确授权目标", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	goalParentID := mustTaskID(t, goalParent.ID)
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, goalParentID) })

	constraintParent, err := m.CreateTaskWithOptions("scope gate constraint parent", "授权任务：仅测试明确授权目标", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	constraintParentID := mustTaskID(t, constraintParent.ID)
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, constraintParentID) })
	if _, err := constraintParent.Store.AddConstraint("deny", "禁止内网渗透", "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM tasks WHERE parent_ref=$1 OR parent_ref=$2`, goalParent.ID, constraintParent.ID)
	})

	spawn := func(payload map[string]any) (isErr bool, text string) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.toolSpawnTask().Call(context.Background(), raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result.IsError, result.Flatten()
	}
	countChildren := func(parentID string) int {
		t.Helper()
		var n int
		if err := m.pg.QueryRow(`SELECT count(*) FROM tasks WHERE parent_ref=$1`, parentID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	countDescription := func(description string) int {
		t.Helper()
		var n int
		if err := m.pg.QueryRow(`SELECT count(*) FROM tasks WHERE description=$1`, description).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	assertNoChild := func(parent *Task, description, label string) {
		t.Helper()
		if n := countChildren(parent.ID); n != 0 {
			t.Fatalf("%s 被拦截后仍创建了子任务: %d", label, n)
		}
		if n := countDescription(description); n != 0 {
			t.Fatalf("%s 被拦截后仍创建了任务行: %d", label, n)
		}
	}
	assertOneChild := func(parent *Task, description, label, wantGoal string) int64 {
		t.Helper()
		if n := countChildren(parent.ID); n != 1 {
			t.Fatalf("%s 正常路径应创建恰好一个子任务, got %d", label, n)
		}
		var childID int64
		if err := m.pg.QueryRow(`SELECT id FROM tasks WHERE description=$1`, description).Scan(&childID); err != nil {
			t.Fatal(err)
		}
		var gotGoal, gotParent string
		if err := m.pg.QueryRow(`SELECT goal,parent_ref FROM tasks WHERE id=$1`, childID).Scan(&gotGoal, &gotParent); err != nil {
			t.Fatal(err)
		}
		if gotGoal != wantGoal {
			t.Fatalf("%s 子任务 goal=%q, want %q", label, gotGoal, wantGoal)
		}
		if gotParent != parent.ID {
			t.Fatalf("%s 子任务 parent_ref=%q, want %q", label, gotParent, parent.ID)
		}
		return childID
	}

	goalBlockedDesc := fmt.Sprintf("scope gate goal blocked %d", stamp)
	isErr, text := spawn(map[string]any{
		"description": goalBlockedDesc,
		"goal":        "执行内网渗透横向",
		"parent_ref":  goalParent.ID,
	})
	if !isErr || !strings.Contains(text, "父任务约束") || !strings.Contains(text, "禁止内网渗透") {
		t.Fatalf("goal 禁止行未拦截: isErr=%v text=%s", isErr, text)
	}
	assertNoChild(goalParent, goalBlockedDesc, "goal 禁止行")

	constraintBlockedDesc := fmt.Sprintf("scope gate constraint blocked %d", stamp)
	isErr, text = spawn(map[string]any{
		"description": constraintBlockedDesc,
		"goal":        "执行内网渗透横向",
		"parent_ref":  constraintParent.ID,
	})
	if !isErr || !strings.Contains(text, "操作约束") || !strings.Contains(text, "禁止内网渗透") {
		t.Fatalf("deny 约束未拦截: isErr=%v text=%s", isErr, text)
	}
	assertNoChild(constraintParent, constraintBlockedDesc, "deny 约束")

	// 父任务 goal 仍有「禁止内网渗透」，但子任务方向不匹配：不得额外拦截。
	goalAllowedDesc := fmt.Sprintf("scope gate goal allowed %d", stamp)
	goalAllowed := "验证 XSS 反射"
	isErr, text = spawn(map[string]any{
		"description": goalAllowedDesc,
		"goal":        goalAllowed,
		"parent_ref":  goalParent.ID,
	})
	if isErr {
		t.Fatalf("goal 无匹配方向被误拦: %s", text)
	}
	if !strings.HasPrefix(text, "task created: ") {
		t.Fatalf("goal 正常路径返回异常: %s", text)
	}
	assertOneChild(goalParent, goalAllowedDesc, "goal 正常路径", goalAllowed)

	// deny 约束存在但子任务方向不匹配：不得额外拦截。
	constraintAllowedDesc := fmt.Sprintf("scope gate constraint allowed %d", stamp)
	constraintAllowed := "验证 SQL 注入"
	isErr, text = spawn(map[string]any{
		"description": constraintAllowedDesc,
		"goal":        constraintAllowed,
		"parent_ref":  constraintParent.ID,
	})
	if isErr {
		t.Fatalf("deny 约束无匹配方向被误拦: %s", text)
	}
	assertOneChild(constraintParent, constraintAllowedDesc, "deny 约束正常路径", constraintAllowed)
}

// ensureScopeGateSpawnTemplates makes the default planner/worker template chain
// available even on a fresh test database; spawn_task snapshots those bodies
// before creating a child task.
func ensureScopeGateSpawnTemplates(t *testing.T, pg *db.DB) {
	t.Helper()
	seeds := agent.BuiltinPromptSeeds()
	if body, err := pg.DefaultPlannerTemplateBody(); err != nil {
		t.Fatal(err)
	} else if strings.TrimSpace(body) == "" {
		nkey := fmt.Sprintf("__scope_gate_planner_%d", time.Now().UnixNano())
		inserted, err := pg.SeedPlannerTemplateIfEmpty(nkey, nkey, seeds["planner"], true)
		if err != nil {
			if body, _ := pg.DefaultPlannerTemplateBody(); strings.TrimSpace(body) == "" {
				t.Fatal(err)
			}
		} else if inserted {
			t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM planner_templates WHERE nkey=$1`, nkey) })
		}
	}
	if body, err := pg.DefaultWorkerTemplateBody(); err != nil {
		t.Fatal(err)
	} else if strings.TrimSpace(body) == "" {
		nkey := fmt.Sprintf("__scope_gate_worker_%d", time.Now().UnixNano())
		inserted, err := pg.SeedWorkerTemplateIfEmpty(nkey, nkey, seeds["worker"], true)
		if err != nil {
			if body, _ := pg.DefaultWorkerTemplateBody(); strings.TrimSpace(body) == "" {
				t.Fatal(err)
			}
		} else if inserted {
			t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM worker_templates WHERE nkey=$1`, nkey) })
		}
	}
}
