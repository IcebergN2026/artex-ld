// llmrec.go：llm.Provider 的录制装饰器，拦截每次调用并把完整请求/响应持久化到数据库。
// 主要函数/类型：
//   - Recorder / Wrap：录制装饰器及其构造，可带录制开关谓词。
//   - Stream / Complete：录制流式与非流式补全调用。
//   - record / recordUsage：把请求响应正文与用量落库的核心路径。
//   - WithTaskID / TaskIDFrom：在上下文中传递任务注册表 ID。
//   - WithWorker：覆盖调用的 agent 泳道标签（如 judge）。

// Package llmrec implements a recording decorator for llm.Provider. It intercepts
// every Stream call, captures the full request and accumulated response, and
// persists them to PostgreSQL for later inspection.
package llmrec

import (
	"context"
	"encoding/json"
	"iter"
	"log"
	"strings"
	"time"

	"artex/db"
	"norma/llm"
	"norma/transcript"
)

type taskIDContextKey struct{}
type workerContextKey struct{}

// WithTaskID attaches the owning task registry id to an LLM call. Session ids
// are based on exploration ids, which are not interchangeable with task ids.
func WithTaskID(ctx context.Context, taskID string) context.Context {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, taskIDContextKey{}, taskID)
}

// TaskIDFrom returns the explicit task registry id attached by the task runtime.
func TaskIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	taskID, _ := ctx.Value(taskIDContextKey{}).(string)
	return strings.TrimSpace(taskID)
}

// WithWorker overrides the agent-lane ("worker") label for an LLM call. The lane
// is normally parsed from the transcript session id (exp<N>-<role>); calls made
// outside the engine's worker/planner sessions — e.g. the intercept fallback judge
// — carry no such session, so they attach their lane explicitly here. This lets the
// usage ledger single out that spend (worker='judge') for the config page.
func WithWorker(ctx context.Context, worker string) context.Context {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return ctx
	}
	return context.WithValue(ctx, workerContextKey{}, worker)
}

// workerFrom returns the explicit lane override, or "" when none is set.
func workerFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	worker, _ := ctx.Value(workerContextKey{}).(string)
	return strings.TrimSpace(worker)
}

// Recorder wraps an llm.Provider and records every completion call.
type Recorder struct {
	inner llm.Provider
	pg    *db.DB
	model string // model name (from config, not in CompletionRequest)
	prof  string // LLM profile name (from llm_profiles)
	// thinkingType / reasoningEffort 是配置级思考参数(思考开关 / 思考强度)。它们在
	// norma 的 buildBody() 里从 provider 配置注入真正的 HTTP body,不出现在
	// CompletionRequest 上,故 Recorder 需在此单独带一份,序列化时写进录制。
	thinkingType    string
	reasoningEffort string
	enabled         func() bool // reports whether recording is currently on; nil = always record
}

// Wrap returns a Provider that records calls to pg when enabled() reports true.
// profName is the LLM profile name (e.g. "default"); may be empty. thinkingType /
// reasoningEffort are the config-level thinking params actually sent to the API
// (empty = not sent). A nil enabled predicate records unconditionally.
func Wrap(inner llm.Provider, pg *db.DB, model, profName, thinkingType, reasoningEffort string, enabled func() bool) *Recorder {
	return &Recorder{
		inner: inner, pg: pg, model: model, prof: profName,
		thinkingType: thinkingType, reasoningEffort: reasoningEffort, enabled: enabled,
	}
}

// parseSession extracts task id and worker role from session strings like
// "exp1-worker-i3" → ("1", "worker") or "exp2-planner" → ("2", "planner").
func parseSession(s string) (taskID, worker string) {
	// format: exp<N>-<role>[-suffix]
	if !strings.HasPrefix(s, "exp") {
		return "", ""
	}
	rest := s[3:] // after "exp"
	// split task number
	i := strings.IndexByte(rest, '-')
	if i < 0 {
		return rest, ""
	}
	taskID = rest[:i]
	rest = rest[i+1:]
	// worker role is up to the next '-' (e.g. "worker" from "worker-i3")
	if j := strings.IndexByte(rest, '-'); j >= 0 {
		worker = rest[:j]
	} else {
		worker = rest
	}
	return taskID, worker
}

// recordPrepState 是录制准备的公共产物（Stream/Complete 同款字段集）。
type recordPrepState struct {
	recordBodies bool
	start        time.Time
	session      string
	taskID       string
	worker       string
	expID        int64
	reqBody      string
	capt         *Capture
}

// recordPrep 录制准备公共块：开关判定（llm_record）、session 解析（taskID/worker/expID）、
// 开闸时的请求序列化 + wire Capture 挂接（返回更新后的 ctx）。Stream/Complete 逐行相同的段落收敛于此。
func (r *Recorder) recordPrep(ctx context.Context, req llm.CompletionRequest) (context.Context, recordPrepState) {
	var rec recordPrepState
	rec.recordBodies = r.enabled == nil || r.enabled()
	rec.start = time.Now()
	rec.session = transcript.SessionIDFrom(ctx)
	parsedID, worker := parseSession(rec.session)
	if ov := workerFrom(ctx); ov != "" {
		worker = ov
	}
	rec.worker = worker
	rec.expID = db.ParseExpID(parsedID)
	rec.taskID = TaskIDFrom(ctx)
	if rec.taskID == "" {
		// Backward compatibility for non-task callers. For task calls, the task
		// runtime always supplies the registry id explicitly.
		rec.taskID = parsedID
	}
	if rec.recordBodies {
		rec.reqBody = r.serializeRequest(req)
		ctx, rec.capt = NewCapture(ctx)
	}
	return ctx, rec
}

// Stream implements llm.Provider. It delegates to the inner provider, accumulates
// the streamed events to reconstruct the response, and records the full exchange.
//
// The session identifier (e.g. "exp1-worker-i3") is carried on ctx by the norma
// harness via transcript.WithSessionID; reading it per-call is race-free even when
// planner and multiple workers share one Recorder instance.
func (r *Recorder) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	// Body recording (the heavy debug trace: full request/response) is gated by the
	// llm_record setting. Lightweight usage metering always runs — it powers token
	// stats and must be complete even for interrupted/failed runs, so it is NOT
	// gated. Only body serialization + response accumulation are skipped when off.
	ctx, rec := r.recordPrep(ctx, req)
	recordBodies, start, session := rec.recordBodies, rec.start, rec.session
	taskID, worker, expID := rec.taskID, rec.worker, rec.expID
	reqBody, capt := rec.reqBody, rec.capt

	return func(yield func(llm.StreamEvent, error) bool) {
		var (
			textBuf     strings.Builder
			thinkingBuf strings.Builder
			usage       llm.Usage
			stopReason  string
			streamErr   error
		)

		finished := false
		finish := func(err error) {
			if finished {
				return
			}
			finished = true
			status := "ok"
			if err != nil {
				status = "error"
			}
			// Lightweight metering row — always written.
			r.recordUsage(taskID, expID, worker, usage, int(time.Since(start).Milliseconds()), status)
			// Heavy trace row — only when body recording is on.
			if recordBodies {
				r.record(req, session, taskID, worker, reqBody, capt, start, textBuf.String(), thinkingBuf.String(), usage, stopReason, err)
			}
		}
		defer func() { finish(ctx.Err()) }()

		for ev, err := range r.inner.Stream(ctx, req) {
			if err != nil {
				streamErr = err
				finish(streamErr)
				if !yield(ev, err) {
					return
				}
				return
			}
			// Always track usage (cheap); accumulate text/thinking only for bodies.
			// Anthropic (and the other providers) split token usage across events:
			// message_start carries ONLY the input side (input + cache), message_delta
			// ONLY the output. They must be FOLDED with Add — overwriting on delta
			// would zero out the input already counted at start (mirrors the SDK's own
			// llm.Accumulator; see norma/llm/accumulate.go).
			switch ev.Type {
			case llm.SETextDelta:
				if recordBodies {
					textBuf.WriteString(ev.Text)
				}
			case llm.SEThinkingDelta:
				if recordBodies {
					thinkingBuf.WriteString(ev.Text)
				}
			case llm.SEMessageStart:
				usage.Add(ev.Usage)
			case llm.SEMessageDelta:
				if ev.StopReason != "" {
					stopReason = ev.StopReason
				}
				usage.Add(ev.Usage)
			}
			if !yield(ev, err) {
				return
			}
		}

		// Stream completed normally.
		finish(streamErr)
	}
}

// Complete implements the atomic completion path while preserving the same
// usage metering, normalized body recording and raw wire capture as Stream.
func (r *Recorder) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	ctx, rec := r.recordPrep(ctx, req)
	recordBodies, start, session := rec.recordBodies, rec.start, rec.session
	taskID, worker, expID := rec.taskID, rec.worker, rec.expID
	reqBody, capt := rec.reqBody, rec.capt

	msg, stopReason, usage, err := r.inner.Complete(ctx, req)
	status := "ok"
	if err != nil {
		status = "error"
	}
	r.recordUsage(taskID, expID, worker, usage, int(time.Since(start).Milliseconds()), status)
	if recordBodies {
		r.record(req, session, taskID, worker, reqBody, capt, start, msg.Text(), thinkingText(msg), usage, stopReason, err)
	}
	return msg, stopReason, usage, err
}

func thinkingText(msg llm.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if block.Type == llm.BlockThinking && block.Thinking != "" {
			b.WriteString(block.Thinking)
		}
	}
	return b.String()
}

// recordUsage appends one lightweight metering row to llm_usage (no bodies). Skips
// zero-token calls with no model, which carry nothing worth metering.
func (r *Recorder) recordUsage(taskID string, expID int64, worker string, usage llm.Usage, latencyMs int, status string) {
	if r.pg == nil {
		return
	}
	if r.model == "" && usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 {
		return
	}
	err := r.pg.InsertLLMUsage(&db.LLMUsage{
		TaskID:        taskID,
		ExplorationID: expID,
		Worker:        worker,
		Model:         r.model,
		ProfileName:   r.prof,
		LatencyMs:     latencyMs,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		CacheRead:     usage.CacheReadTokens,
		CacheWrite:    usage.CacheWriteTokens,
		Status:        status,
	})
	if err != nil {
		log.Printf("[llmusage] insert: %v", err)
	}
}

// record persists one LLM call to PostgreSQL before the provider stream returns.
// Keeping the write inside the owning task operation means task deletion can
// drain calls and then remove records without a late async insert recreating one.
func (r *Recorder) record(req llm.CompletionRequest, session, taskID, worker, reqBody string, capt *Capture, start time.Time, text, thinking string, usage llm.Usage, stopReason string, streamErr error) {
	latency := int(time.Since(start).Milliseconds())
	status := "ok"
	errMsg := ""
	if streamErr != nil {
		status = "error"
		errMsg = streamErr.Error()
	}

	// Build response body JSON.
	resp := map[string]any{
		"text":        text,
		"stop_reason": stopReason,
		"usage": map[string]int{
			"input_tokens":       usage.InputTokens,
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
		},
	}
	if thinking != "" {
		resp["thinking"] = thinking
	}
	respBody, _ := json.Marshal(resp)

	rec := &db.LLMRecord{
		SessionID:    session,
		TaskID:       taskID,
		Worker:       worker,
		Model:        r.model,
		ProfileName:  r.prof,
		LatencyMs:    latency,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		CacheRead:    usage.CacheReadTokens,
		CacheWrite:   usage.CacheWriteTokens,
		Status:       status,
		Error:        errMsg,
		RequestBody:  reqBody,
		ResponseBody: string(respBody),
		// Raw wire bodies; empty when the transport did not fill the Capture
		// (e.g. a provider dialing through a client without the capture hook, or
		// a call that failed before any HTTP request went out).
		RawRequest:  capt.RawRequest(),
		RawResponse: capt.RawResponse(),
	}

	if err := r.pg.InsertLLMRecord(rec); err != nil {
		log.Printf("[llmrec] insert: %v", err)
	}
}

// serializeRequest builds a JSON representation of the completion request.
func (r *Recorder) serializeRequest(req llm.CompletionRequest) string {
	m := map[string]any{
		"system":     req.System,
		"messages":   req.Messages,
		"max_tokens": req.MaxTokens,
	}
	// 记录本次调用实际发出的思考参数。type 采用「有效值」：每请求覆盖 req.Thinking
	// 优先于配置级 thinkingType(与 norma buildBody 的判定一致，如 compaction 摘要会
	// 强制 disabled)；effort 无每请求覆盖，直接取配置值。两者皆空则不写 thinking 字段。
	effType := r.thinkingType
	if req.Thinking != "" {
		effType = req.Thinking
	}
	if effType != "" || r.reasoningEffort != "" {
		m["thinking"] = map[string]string{"type": effType, "effort": r.reasoningEffort}
	}
	if len(req.Tools) > 0 {
		// Store tool names only (full schemas are huge).
		names := make([]string, len(req.Tools))
		for i, t := range req.Tools {
			names[i] = t.Name
		}
		m["tools"] = names
		m["tools_count"] = len(req.Tools)
	}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if len(req.Stop) > 0 {
		m["stop"] = req.Stop
	}
	b, _ := json.Marshal(m)
	return string(b)
}
