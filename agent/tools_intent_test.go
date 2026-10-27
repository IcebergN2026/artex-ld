package agent

// ═══ ARTEX 二开 · 审核流 PR4 工具行为测试 ═══
// 两层覆盖：
//  1. nil store：needExploration 守卫必须先于一切逻辑触发，工具返回
//     「需要任务上下文」业务错误而不是 panic（无 PG 也能跑）。
//  2. 真 PG（ARTEX_PG_DSN，无库自动 skip）：参数校验（expected_version 必填、
//     至少一个修改字段、target_executor 校验器）必须先于任何 SQL 执行——
//     用不存在的 intent_id 调用，若先碰库会报「意图不存在」而非参数错误。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func callTool(t *testing.T, tool actool.CoreTool, in string) actool.Result {
	t.Helper()
	res, err := tool.Call(context.Background(), json.RawMessage(in), nil)
	if err != nil {
		t.Fatalf("%s returned transport error: %v", tool.Name(), err)
	}
	return res
}

func wantToolError(t *testing.T, tool actool.CoreTool, in, substr string) {
	t.Helper()
	res := callTool(t, tool, in)
	if !res.IsError {
		t.Fatalf("%s on %s: want tool error, got success %q", tool.Name(), in, res.Flatten())
	}
	if !strings.Contains(res.Flatten(), substr) {
		t.Fatalf("%s on %s: error %q missing %q", tool.Name(), in, res.Flatten(), substr)
	}
}

// ---- 第 1 层：nil store 守卫 ----

func TestIntentEditToolsNilStoreGuard(t *testing.T) {
	ts := NewToolSet(nil, "planner")
	in := `{"intent_id":1,"expected_version":0,"summary":"x","reason":"r"}`
	wantToolError(t, ts.updateIntent(), in, "任务上下文")
	wantToolError(t, ts.cancelIntent(), in, "任务上下文")
	// review 对话同样被守卫拦住（非 human 跳过 open 预检，但守卫在最前）。
	ts2 := NewToolSet(nil, "review")
	wantToolError(t, ts2.updateIntent(), in, "任务上下文")
}

// ---- 第 2 层：真 PG 下的参数校验顺序 ----

// newIntentToolTestSet 建独立 exploration，返回 planner ToolSet 与清理函数。
func newIntentToolTestSet(t *testing.T, worker string) (*ToolSet, func()) {
	t.Helper()
	d := testDB(t)
	expID, err := d.CreateExploration("审核流工具参数校验测试", "隔离环境")
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	ts := NewToolSet(d.Exploration(expID), worker)
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		d.Close()
	}
	return ts, cleanup
}

func TestUpdateIntentParamValidation(t *testing.T) {
	ts, cleanup := newIntentToolTestSet(t, "planner")
	defer cleanup()
	tool := ts.updateIntent()

	wantToolError(t, tool, `{`, "invalid input")
	wantToolError(t, tool, `{}`, "intent_id")
	wantToolError(t, tool, `{"intent_id":1,"summary":"x"}`, "expected_version 必填")
	wantToolError(t, tool, `{"intent_id":1,"expected_version":0}`, "至少要传一个待修改字段")
	wantToolError(t, tool, `{"intent_id":-3,"expected_version":1,"summary":"x"}`, "intent_id")
	// 参数全合法但意图不存在 → 走到 DB 层报不存在（证明参数校验全部通过）。
	wantToolError(t, tool,
		`{"intent_id":99999999,"expected_version":0,"summary":"x"}`,
		"not found")
}

func TestCancelIntentParamValidation(t *testing.T) {
	ts, cleanup := newIntentToolTestSet(t, "planner")
	defer cleanup()
	tool := ts.cancelIntent()

	wantToolError(t, tool, `{`, "invalid input")
	wantToolError(t, tool, `{}`, "intent_id")
	wantToolError(t, tool, `{"intent_id":2,"reason":"x"}`, "expected_version 必填")
	wantToolError(t, tool, `{"intent_id":99999999,"expected_version":0,"reason":"r"}`, "not found")
}

// target_executor 校验器必须先于 DB 写：注册总拒绝的 checker，用不存在的
// intent_id 调用，若 checker 后于库查询会报「意图不存在」而非 target_executor 错误。
func TestUpdateIntentExecutorCheckBeforeStore(t *testing.T) {
	ts, cleanup := newIntentToolTestSet(t, "planner")
	defer cleanup()
	ts.SetExecutorChecker(func(name string) error { return fmt.Errorf("未注册") })
	tool := ts.updateIntent()
	wantToolError(t, tool,
		`{"intent_id":99999999,"expected_version":0,"target_executor":"nope"}`,
		"target_executor")
}

// review_enabled=false 时送审必须明确失败（§2.7 不静默直投 open）。
func TestAddReviewIntentRefusedWhenReviewDisabled(t *testing.T) {
	ts := NewToolSet(nil, "planner")
	ts.SetReviewEnabled(func() bool { return false })
	tool := ts.addReviewIntent()
	res := callTool(t, tool, `{"summary":"测试送审"}`)
	if !res.IsError {
		t.Fatalf("want refusal when review disabled, got %q", res.Flatten())
	}
}

// PG 下送审闭环：add_review_intent 落 proposed+review_status=pending（PR1 内核）。
func TestAddReviewIntentLandsPending(t *testing.T) {
	ts, cleanup := newIntentToolTestSet(t, "planner")
	defer cleanup()
	ts.SetReviewEnabled(func() bool { return true })
	res := callTool(t, ts.addReviewIntent(), `{"summary":"送审闭环验证","priority":3}`)
	if res.IsError {
		t.Fatalf("add_review_intent failed: %s", res.Flatten())
	}
	var out struct {
		IntentID int64 `json:"intent_id"`
		Created  bool  `json:"created"`
		Proposed bool  `json:"proposed"`
	}
	if err := json.Unmarshal([]byte(res.Flatten()), &out); err != nil || out.IntentID == 0 || !out.Created || !out.Proposed {
		t.Fatalf("bad result: %q err=%v", res.Flatten(), err)
	}
	n, err := ts.ts.GetNodeWithSources(out.IntentID)
	if err != nil || n == nil {
		t.Fatalf("node not found: %v", err)
	}
	if n.State != "proposed" {
		t.Fatalf("state = %q, want proposed", n.State)
	}
	var p map[string]any
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["executor"] != "review" || p["review_status"] != "pending" {
		t.Fatalf("payload executor=%v review_status=%v, want review/pending", p["executor"], p["review_status"])
	}
	if _, ok := p["replaces"]; ok {
		t.Fatalf("payload must not carry replaces key: %v", p)
	}
}

var _ = db.ErrStaleVersion // 占位：db 包引用（未来 CAS 行为测试扩展点）
