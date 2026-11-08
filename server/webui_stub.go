//go:build !embedui

// webui_stub.go：默认（非 embedui）构建下前端未内嵌时的 404 桩 handler。
// 主要函数/类型：
//   - webuiHandler：返回「前端未内嵌」404 提示的占位实现。

package server

import "net/http"

// webuiHandler is the no-embed stub (default build). The frontend is NOT bundled
// into this binary — run it separately with `cd web && npm run dev` during
// development. Build the bundled single-binary with:
//
//	cd web && npm run build:static     # produces web/out
//	cp -r web/out server/webui/dist    # (or use the build script)
//	go build -tags embedui ./cmd/artex
func (s *Server) webuiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "前端未内嵌到此二进制（开发用 next dev；发布用 -tags embedui 构建）", http.StatusNotFound)
	})
}
