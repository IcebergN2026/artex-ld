#!/usr/bin/env bash
# ARTEX 本地构建段共享库：install.sh 的「本地编译运行」与 update.sh 的
# 「本地编译更新」构建段原本逐字重复，收敛到 build_embedded() 单点维护。
# build.sh 的 CI 变体（rsync 增量同步、可跳 npm ci）不走这里，保持不动。
#
# 用法（在调用方脚本内 source 后调用）：
#   . ./scripts/lib-build.sh
#   build_embedded "构建前端静态产物…" "编译内嵌单二进制…"
# 两条 info 文案由参数传入——install.sh 与 update.sh 用词不同
# （构建/编译 vs 重建/重新编译），须保持各自输出逐字不变。
build_embedded() {
	local _frontend_msg="$1" _build_msg="$2"
	info "$_frontend_msg"
	( cd web && npm ci && npm run build:static )
	mkdir -p server/webui/dist
	rm -rf server/webui/dist
	cp -r web/out server/webui/dist
	info "$_build_msg"
	CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
}
