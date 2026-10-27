package server

// ═══ ARTEX 二开 · [P-LOG·M0 HTTP 访问日志中间件] ═══
// 只对 4xx/5xx 与 >2s 慢请求打日志（2xx 快请求不记，防刷屏）；每请求生成 req id
// 注入 context，配合 writeErr 的 5xx 日志可双向对照。
// 二开日期：2026-10-25

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/Autumn-27/artex/xlog"
)

type ctxKeyReqID struct{}

// ReqIDFrom 取访问日志中间件注入的 req id（无则空串）。
func ReqIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyReqID{}).(string)
	return v
}

// statusWriter 记录响应状态码（默认 200）。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush 透传 http.Flusher——SSE 端点（/api/logs/stream 等）依赖它推送事件，
// 不实现会让 w.(http.Flusher) 断言失败、SSE 静默断流。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// accessLog 包装 /api 处理器：4xx/5xx 与慢请求（>2s）各记一行。
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		reqID := hex.EncodeToString(b)
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxKeyReqID{}, reqID)))
		dur := time.Since(start)
		switch {
		case sw.status >= 500:
			xlog.Errorf("[api] %s %s → %d req=%s dur=%dms", r.Method, r.URL.Path, sw.status, reqID, dur.Milliseconds())
		case sw.status >= 400:
			xlog.Warnf("[api] %s %s → %d req=%s dur=%dms", r.Method, r.URL.Path, sw.status, reqID, dur.Milliseconds())
		case dur > 2*time.Second:
			xlog.Infof("[api] slow %s %s → %d req=%s dur=%dms", r.Method, r.URL.Path, sw.status, reqID, dur.Milliseconds())
		}
	})
}
