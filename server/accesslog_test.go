package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 防回归：accessLog 的 statusWriter 必须透传 http.Flusher，
// 否则 SSE 端点（/api/logs/stream 等）断言 w.(http.Flusher) 失败、静默断流。
func TestAccessLogPreservesFlusher(t *testing.T) {
	var sawFlusher bool
	h := accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil))
	if !sawFlusher {
		t.Fatal("accessLog wrapper hid http.Flusher — SSE endpoints would break")
	}
}
