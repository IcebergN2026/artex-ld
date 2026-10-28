package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
)

// TestReviewChatLongReplyEndToEnd 走真实 reviewChat HTTP 入口、异步 RunReviewChat、
// Anthropic SSE provider、activity 落库和 reviewMessages HTTP 出口，验证超长多行
// 回复逐字返回，而不是只覆盖手工 INSERT activity 的读取路径。
func TestReviewChatLongReplyEndToEnd(t *testing.T) {
	s, h, token, task := setupReviewHTTPServer(t)
	s.ctx = context.Background()
	s.provByProfile = map[int64]*provEntry{}
	task.Guard = guard.New()
	task.Goal = "验证审核会话长回复完整性"

	prefix := "## 审核分析\n\n"
	body := strings.Repeat("发现项：当前证据需要保留完整上下文，不能只显示第一行或摘要。\n", 12)
	suffix := "\n**最终结论：回复末尾必须可见。**\nREVIEW_REPLY_END"
	fullReply := prefix + body + suffix
	if len(fullReply) <= 400 || !strings.Contains(fullReply, "\n") {
		t.Fatalf("invalid long-reply fixture: %d bytes", len(fullReply))
	}

	var providerCalls atomic.Int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		if r.URL.Path != "/v1/messages" {
			t.Errorf("provider path=%q, want /v1/messages", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvent := func(event string, payload any) {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Errorf("marshal SSE payload: %v", err)
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		}
		writeEvent("message_start", map[string]any{
			"type":    "message_start",
			"message": map[string]any{"id": "msg_review_long", "usage": map[string]any{"input_tokens": 20, "output_tokens": 1}},
		})
		writeEvent("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		for i, chunk := range []string{prefix, body, suffix} {
			writeEvent("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": chunk},
			})
			if f, ok := w.(http.Flusher); ok && i == 0 {
				f.Flush()
			}
		}
		writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		writeEvent("message_delta", map[string]any{
			"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 300},
		})
		writeEvent("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer mockLLM.Close()

	profileID, err := s.m.pg.SaveProfile(&db.LLMProfile{
		Name: "review-long-reply-mock", Format: "anthropic", BaseURL: mockLLM.URL,
		Model: "claude-review-test", APIKey: "test-key", Streaming: true,
	})
	if err != nil {
		t.Fatalf("save mock LLM profile: %v", err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, profileID) })
	task.LLMProfileIDs = []int64{profileID}
	res, err := s.m.pg.Exec(`UPDATE agents SET enabled=true WHERE key='review'`)
	if err != nil {
		t.Fatalf("enable review agent: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("review agent seed rows affected=%d, want 1", n)
	}

	const userMessage = "请给出完整的多行审核分析"
	rec := doAuthed(t, h, token, http.MethodPost,
		fmt.Sprintf("/api/tasks/%s/review/chat", task.ID),
		fmt.Sprintf(`{"message":%q}`, userMessage))
	if rec.Code != 202 {
		t.Fatalf("review chat status=%d body=%s", rec.Code, rec.Body.String())
	}

	type message struct {
		ID      int64  `json:"id"`
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
		Detail  string `json:"detail"`
		IsError bool   `json:"is_error"`
	}
	deadline := time.Now().Add(15 * time.Second)
	var lastMessages []message
	for time.Now().Before(deadline) {
		rec = doAuthed(t, h, token, http.MethodGet,
			fmt.Sprintf("/api/tasks/%s/review/messages", task.ID), "")
		if rec.Code != 200 {
			t.Fatalf("review messages status=%d body=%s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Messages []message `json:"messages"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode review messages: %v", err)
		}
		lastMessages = payload.Messages
		foundUser, foundResult := false, false
		for _, m := range lastMessages {
			if m.IsError {
				t.Fatalf("review returned error activity: kind=%s detail=%s", m.Kind, m.Detail)
			}
			if m.Kind == "user" && m.Detail == userMessage {
				foundUser = true
			}
			if m.Kind == "result" && m.Detail == fullReply {
				foundResult = true
				if !strings.HasSuffix(m.Detail, "REVIEW_REPLY_END") {
					t.Fatal("reply end marker missing")
				}
			}
		}
		if foundUser && foundResult {
			if providerCalls.Load() == 0 {
				t.Fatal("mock provider was not called")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for full review reply; provider calls=%d messages=%+v", providerCalls.Load(), lastMessages)
}
