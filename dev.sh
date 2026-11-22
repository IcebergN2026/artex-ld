#!/usr/bin/env bash
# 开发模式：后端(:8787) + 流量代理(:8788) 与前端 Next.js dev(:3000) 一起运行。
# 单二进制（前端内嵌）方式见 README「单二进制」一节，不走这个脚本。
set -euo pipefail
cd "$(dirname "$0")"

backend_pid=""
frontend_pid=""
cleanup() {
  trap - EXIT INT TERM
  # 只终止本脚本启动的两个进程；kill 0 会误杀调用方所在的整个进程组。
  if [[ -n "$frontend_pid" ]]; then kill "$frontend_pid" 2>/dev/null || true; fi
  if [[ -n "$backend_pid" ]]; then kill "$backend_pid" 2>/dev/null || true; fi
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# 后端（普通 go run，不内嵌前端）；并发 work agent 数在「系统设置」里配置。
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &
backend_pid=$!

# 前端热更新（Next.js dev server，/api 反代到 :8787）。
( cd web && npm run dev ) &
frontend_pid=$!

echo "[dev] 后端 :8787 / 代理 :8788 / 前端 http://localhost:3000  (Ctrl-C 退出)"
wait
