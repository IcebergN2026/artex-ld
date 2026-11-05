package mcphttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLegacySSEClientDiscoverAndCall(t *testing.T) {
	var mu sync.Mutex
	var stream http.ResponseWriter
	var flusher http.Flusher
	sessions := make(map[string]bool)
	messages := make(chan []byte, 8)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sse":
			id := "test-session"
			mu.Lock()
			sessions[id] = true
			stream, flusher = w, w.(http.Flusher)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fmt.Fprintf(w, "event: endpoint\ndata: %s/message?sessionId=%s\n\n", "http://"+r.Host, id)
			flusher.Flush()
			for {
				select {
				case payload := <-messages:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
					flusher.Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/message":
			mu.Lock()
			knownSession := sessions["test-session"]
			mu.Unlock()
			if r.URL.Query().Get("sessionId") != "test-session" || !knownSession {
				http.Error(w, "unknown session", http.StatusBadRequest)
				return
			}
			var req rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			if req.ID == 0 {
				return
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "echo input", "inputSchema": map[string]any{"type": "object"}}}}
			case "tools/call":
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
			default:
				result = map[string]any{}
			}
			resultJSON, err := json.Marshal(result)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			payload, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(resultJSON)})
			mu.Lock()
			streamReady := stream != nil
			mu.Unlock()
			if streamReady {
				messages <- payload
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	c, err := NewSSE(setupCtx, "test", srv.URL+"/sse", map[string]string{"Authorization ": "Bearer test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancelSetup() // 初始化结束后，setup ctx 的取消不应关闭长驻 SSE。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || !strings.Contains(tools[0].Name(), "echo") {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	got, err := c.Call(ctx, "echo", map[string]any{"value": "x"})
	if err != nil || got != "ok" {
		t.Fatalf("Call() = %q, %v", got, err)
	}
}

func TestLegacySSEHandshakeHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := NewSSE(ctx, "test", srv.URL, nil, false)
	if c != nil {
		_ = c.Close()
		t.Fatal("handshake without an endpoint must not succeed")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handshake error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("handshake ignored cancellation for %s", elapsed)
	}
}

func TestLegacyRoundTripPreservesStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	// done 和 streamErr 同时就绪时，select 的选择是随机的；重复调用确保覆盖该交错。
	for i := 0; i < 50; i++ {
		want := errors.New("stream parse failed")
		streamErr := make(chan error, 1)
		streamErr <- want
		streamDone := make(chan struct{})
		close(streamDone)
		c := &Client{
			messageURL: srv.URL,
			http:       srv.Client(),
			streamDone: streamDone,
			streamErr:  streamErr,
			pending:    make(map[int]chan *rpcResponse),
		}
		_, err := c.legacyRoundTrip(context.Background(), rpcRequest{
			JSONRPC: "2.0",
			ID:      i + 1,
			Method:  "ping",
		}, true)
		if !errors.Is(err, want) {
			t.Fatalf("iteration %d: error = %v, want original stream error", i, err)
		}
	}
}
