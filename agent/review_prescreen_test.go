package agent

import (
	"context"
	"reflect"
	"testing"

	"norma/llm"
)

func TestRunReviewPreScreenRequiresNote(t *testing.T) {
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		events := []llm.StreamEvent{
			{Type: llm.SETextDelta, Text: `{
				"1": {"verdict": "pass", "note": ""},
				"#2": {"verdict": "revise", "note": " \t\n "},
				"3": {"verdict": "pass", "note": "  符合任务目标，建议放行。  "},
				"#4": {"verdict": "revise", "note": "\n  请补充明确的测试目标。 \t"},
				"5": {"verdict": "reject", "note": "非法预审结论"},
				"999": {"verdict": "pass", "note": "未知意图不进入结果"}
			}`},
			{Type: llm.SEMessageDelta, StopReason: "end_turn"},
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}

	got, err := RunReviewPreScreen(t.Context(), ReviewPreScreenConfig{
		Provider: provider,
		FreshIDs: []int64{1, 2, 3, 4, 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]PreScreenVerdict{
		3: {Verdict: "pass", Note: "符合任务目标，建议放行。"},
		4: {Verdict: "revise", Note: "请补充明确的测试目标。"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pre-screen verdicts = %#v, want %#v", got, want)
	}
}
