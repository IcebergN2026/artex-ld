package server

import (
	"strings"
	"testing"
)

// A long task goal repeated per event was the dominant bloat. These tests pin the
// fix: the task-context header (description + goal) is rendered ONCE per task, no
// matter how many same-task fires are merged.

const longGoal = "拿到题目 f2-05 的受保护 flag 并通过 submit_flag 提交；本题密文已高度收敛，flag 只能由二进制内嵌数据派生……" // 代表那段几千字的继承事实

func sameTaskFires(n int) []triggeredRun {
	items := make([]triggeredRun, n)
	for i := range items {
		items[i] = triggeredRun{
			agentKey: "tec_benchmark", taskID: 72, taskDesc: "f2-05 逆向", taskGoal: longGoal,
			message: "【本次由工具调用触发】\n工具: submit_flag\n入参: {...}\n返回: {correct:false}", mergeable: true,
		}
	}
	return items
}

func TestMergeAllRunsWritesTaskGoalOnce(t *testing.T) {
	out := mergeAllRuns(sameTaskFires(39))
	if got := strings.Count(out.message, longGoal); got != 1 {
		t.Fatalf("same-task goal should appear exactly once in a merged-all run, got %d", got)
	}
	if strings.Count(out.message, "── 触发 ") < 1 || !strings.Contains(out.message, "触发 39") {
		t.Fatalf("all 39 event bodies should be present: %q", out.message)
	}
	// A merged run embeds its header inline, so finalTriggerMessage must not re-add it.
	if out.taskDesc != "" || out.taskGoal != "" {
		t.Fatalf("merged run must clear taskDesc/taskGoal to avoid a duplicate header")
	}
	if finalTriggerMessage(out) != out.message {
		t.Fatalf("finalTriggerMessage must not prepend another header for a merged run")
	}
}

func TestMergeAllRunsGroupsInterleavedTasks(t *testing.T) {
	// Fires from two tasks arriving interleaved (A,B,A,B) must still carry each
	// task's context exactly once — grouping, not per-event repetition.
	mk := func(id int64, goal string) triggeredRun {
		return triggeredRun{agentKey: "a", taskID: id, taskDesc: "d", taskGoal: goal, message: "body", mergeable: true}
	}
	out := mergeAllRuns([]triggeredRun{mk(1, "GOAL_A"), mk(2, "GOAL_B"), mk(1, "GOAL_A"), mk(2, "GOAL_B")})
	if got := strings.Count(out.message, "GOAL_A"); got != 1 {
		t.Fatalf("task #1 goal should appear once despite interleaving, got %d", got)
	}
	if got := strings.Count(out.message, "GOAL_B"); got != 1 {
		t.Fatalf("task #2 goal should appear once despite interleaving, got %d", got)
	}
	if !strings.Contains(out.message, "共 2 个任务") {
		t.Fatalf("header should report 2 tasks: %q", out.message)
	}
	if got := strings.Count(out.message, "── 触发 "); got != 4 {
		t.Fatalf("all 4 event bodies should be present, got %d", got)
	}
}

func TestMergeTriggeredRunsWritesTaskGoalOnce(t *testing.T) {
	out := mergeTriggeredRuns(sameTaskFires(5))
	if got := strings.Count(out.message, longGoal); got != 1 {
		t.Fatalf("same-task goal should appear exactly once in a by-task merge, got %d", got)
	}
}

func TestFinalTriggerMessageSingleFirePrependsHeaderOnce(t *testing.T) {
	item := sameTaskFires(1)[0]
	msg := finalTriggerMessage(item)
	if got := strings.Count(msg, longGoal); got != 1 {
		t.Fatalf("single fire should carry the task goal exactly once, got %d", got)
	}
	if !strings.HasPrefix(msg, "【任务 #72") {
		t.Fatalf("single fire should be prefixed with the task-context header: %q", msg)
	}
}

func TestTaskContextHeaderEmptyForIntervalFire(t *testing.T) {
	if h := taskContextHeader(0, "", ""); h != "" {
		t.Fatalf("interval/none trigger (no task) must produce no header, got %q", h)
	}
	// An interval fire's message must pass through untouched.
	item := triggeredRun{message: "定时触发正文"}
	if finalTriggerMessage(item) != "定时触发正文" {
		t.Fatalf("interval fire message must pass through unchanged")
	}
}

// Planner 显式派发的 run 携带 findingInvocationRef，合并后必须按事件顺序完整保留，
// 否则合并会话无法回填全部 invocation；普通 trigger 的 nil refs 合并后仍必须为 nil。
func fiRefRuns(agentKey string, taskID int64, bodies []string, refs []findingInvocationRef) []triggeredRun {
	items := make([]triggeredRun, len(bodies))
	for i, body := range bodies {
		var inv []findingInvocationRef
		if i < len(refs) && refs[i].findingID != 0 {
			inv = []findingInvocationRef{refs[i]}
		}
		items[i] = triggeredRun{
			agentKey: agentKey, taskID: taskID, taskDesc: "d", taskGoal: "g",
			message: body, mergeable: true, invocations: inv,
		}
	}
	return items
}

func TestMergeTriggeredRunsKeepsInvocationRefsInOrder(t *testing.T) {
	items := fiRefRuns("fitest_a", 7,
		[]string{"body-1", "body-2", "body-3(普通 trigger，无 ref)"},
		[]findingInvocationRef{{agentKey: "fitest_a", findingID: 101}, {agentKey: "fitest_a", findingID: 102}})
	out := mergeTriggeredRuns(items)
	// 同一任务的多条派发合并为一个 run。
	want := []findingInvocationRef{{agentKey: "fitest_a", findingID: 101}, {agentKey: "fitest_a", findingID: 102}}
	if len(out.invocations) != len(want) {
		t.Fatalf("合并后应保留 %d 条 invocation ref，实际 %d", len(want), len(out.invocations))
	}
	for i := range want {
		if out.invocations[i] != want[i] {
			t.Fatalf("ref #%d 顺序错乱: got %+v want %+v", i, out.invocations[i], want[i])
		}
	}
	if out.taskID != 7 || !strings.Contains(out.title, "合并触发") {
		t.Fatalf("same-task 合并应只产生一个 run: %+v", out)
	}
	if strings.Count(out.message, "── 触发 ") != 3 {
		t.Fatalf("合并消息应包含全部 3 条事件体: %q", out.message)
	}
	// 纯普通 trigger 的合并不得凭空产生 refs。
	plain := mergeTriggeredRuns(sameTaskFires(3))
	if plain.invocations != nil {
		t.Fatalf("普通 trigger 合并后 invocations 必须为 nil，got %+v", plain.invocations)
	}
}

func TestMergeAllRunsKeepsInvocationRefsAcrossTasks(t *testing.T) {
	items := append(
		fiRefRuns("fitest_a", 1, []string{"task1-body-1", "task1-body-2"},
			[]findingInvocationRef{{agentKey: "fitest_a", findingID: 11}, {agentKey: "fitest_a", findingID: 12}}),
		fiRefRuns("fitest_a", 2, []string{"task2-body-1", "task2-body-2(普通 trigger，无 ref)"},
			[]findingInvocationRef{{agentKey: "fitest_a", findingID: 21}})...,
	)
	out := mergeAllRuns(items)
	want := []findingInvocationRef{
		{agentKey: "fitest_a", findingID: 11},
		{agentKey: "fitest_a", findingID: 12},
		{agentKey: "fitest_a", findingID: 21},
	}
	if len(out.invocations) != len(want) {
		t.Fatalf("all 合并应保留 %d 条 invocation ref，实际 %d", len(want), len(out.invocations))
	}
	for i := range want {
		if out.invocations[i] != want[i] {
			t.Fatalf("ref #%d 顺序错乱（all 合并必须按事件顺序拼接）: got %+v want %+v", i, out.invocations[i], want[i])
		}
	}
	if strings.Count(out.message, "── 触发 ") != 4 || !strings.Contains(out.message, "共 2 个任务") {
		t.Fatalf("all 合并消息应包含全部 4 条事件体且报告 2 个任务: %q", out.message)
	}
	// 纯普通 trigger 的 all 合并同样不得产生 refs。
	plain := mergeAllRuns(sameTaskFires(2))
	if plain.invocations != nil {
		t.Fatalf("普通 trigger all 合并后 invocations 必须为 nil，got %+v", plain.invocations)
	}
}

func TestTaskContextHeaderTruncatesLongGoal(t *testing.T) {
	huge := strings.Repeat("很", 5000)
	h := taskContextHeader(72, "d", huge)
	if len([]rune(h)) > 800 { // 200 desc + 500 goal + 截断标记/装饰，远小于 5000
		t.Fatalf("header should be bounded even for a huge goal, got %d runes", len([]rune(h)))
	}
}
