#!/usr/bin/env bash
# ARTEX Docker 一键部署：生成配置、从源码构建、启动服务并检查健康状态。
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

COMPOSE_FILE="docker-compose.yml"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-120}"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m[+]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
用法：
  ./deploy.sh              生成缺失的配置，从源码构建并启动 ARTEX
  ./deploy.sh --no-build   使用已有镜像启动（不重新构建）
  ./deploy.sh --help       显示帮助

环境变量：
  HEALTH_TIMEOUT           应用健康检查超时秒数，默认 120

首次运行会自动创建 .env，并生成 16 位随机 PostgreSQL 密码。
已有 .env 不会被覆盖；API Key 可填入 .env，也可部署后在 UI 中配置。
重复执行会保留数据库、data/ 和现有配置。
部署成功后会显示实际生效的 PostgreSQL 密码和 JWT Key，请注意终端与日志安全。
EOF
}

BUILD=1
case "${1:-}" in
  "") ;;
  --no-build) BUILD=0; shift ;;
  --help|-h) usage; exit 0 ;;
  *) die "未知参数：$1（使用 --help 查看用法）" ;;
esac
[ "$#" -eq 0 ] || die "多余参数：$*"
[[ "$HEALTH_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || die "HEALTH_TIMEOUT 必须是正整数"

compose() {
  docker compose -f "$COMPOSE_FILE" "$@"
}

show_diagnostics() {
  compose ps || true
  compose logs --tail=80 artex postgres || true
}

info "检查部署文件"
[ -f "$COMPOSE_FILE" ] || die "找不到 $COMPOSE_FILE"
[ -f Dockerfile.source ] || die "找不到 Dockerfile.source"
[ -f .env.example ] || die "找不到 .env.example"
[ -f start.sh ] || die "找不到 start.sh"
[ -d skills ] || die "找不到 skills 目录"

info "检查 Docker 环境"
command -v docker >/dev/null 2>&1 || die "未检测到 Docker，请先安装 Docker Engine 和 Compose v2"
docker compose version >/dev/null 2>&1 || die "未检测到 Docker Compose v2（命令：docker compose）"
docker info >/dev/null 2>&1 || die "无法连接 Docker 守护进程，请启动 Docker 并检查当前用户权限"

if [ ! -f .env ]; then
  info "首次部署：创建 .env 并生成 16 位随机 PostgreSQL 密码"
  # 使用 16 个十六进制字符，避免密码中的 URL 保留字符破坏 Compose 中的 PG DSN。
  POSTGRES_PASSWORD="$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  [ "${#POSTGRES_PASSWORD}" -eq 16 ] || die "随机密码生成失败"
  (umask 077; sed "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${POSTGRES_PASSWORD}|" .env.example > .env)
  chmod 600 .env
  ok "已创建 .env（权限 0600）"
else
  info "沿用现有 .env"
  if grep -Eq '^POSTGRES_PASSWORD=(change-me-please)?[[:space:]]*$' .env; then
    die "请先在 .env 中设置安全的 POSTGRES_PASSWORD"
  fi
fi

# 提前创建 bind-mount 数据目录，避免 Docker 以 root 身份自动创建。
mkdir -p data

info "校验 Compose 配置"
compose config --quiet

if [ "$BUILD" -eq 1 ]; then
  info "从当前源码构建镜像并启动 PostgreSQL、ARTEX"
  compose up -d --build
else
  info "使用现有镜像启动 PostgreSQL、ARTEX"
  compose up -d
fi

info "等待 ARTEX 健康检查通过（最多 ${HEALTH_TIMEOUT} 秒）"
deadline=$((SECONDS + HEALTH_TIMEOUT))
while [ "$SECONDS" -lt "$deadline" ]; do
  # 镜像内已安装 curl；容器内探测不依赖宿主机是否安装 curl。
  if compose exec -T artex curl -fsS --max-time 5 \
      http://127.0.0.1:8787/api/health >/dev/null 2>&1; then
    # 从容器读取实际生效值，避免 .env 引号或 shell 环境变量覆盖导致展示不一致。
    pg_password="$(compose exec -T postgres sh -c 'printf %s "$POSTGRES_PASSWORD"')" ||
      die "无法从 postgres 容器读取 POSTGRES_PASSWORD"
    jwt_key="$(compose exec -T artex sh -c 'cat /app/jwt.key')" ||
      die "无法从 artex 容器读取 /app/jwt.key"
    [ -n "$pg_password" ] || die "PostgreSQL 密码为空"
    [ "${#jwt_key}" -ge 32 ] || die "JWT Key 无效或尚未生成"

    ok "部署完成，ARTEX 运行正常"
    printf '\n'
    printf 'Web UI         : http://localhost:8787（远程服务器请使用 http://<服务器IP>:8787）\n'
    printf '初始化页面     : http://localhost:8787/setup\n'
    printf 'PostgreSQL 密码: %s\n' "$pg_password"
    printf 'JWT Key        : %s\n' "$jwt_key"
    printf '服务状态       : docker compose ps\n'
    printf '查看日志       : docker compose logs -f artex\n'
    printf '停止服务       : docker compose down（不要加 -v，否则会删除数据库卷）\n'
    exit 0
  fi

  if ! compose ps --status running --services | grep -qx artex; then
    printf '\033[31m[x]\033[0m artex 容器未保持运行，诊断信息如下：\n' >&2
    show_diagnostics
    exit 1
  fi
  sleep 3
done

printf '\033[31m[x]\033[0m 健康检查在 %s 秒后仍未通过，诊断信息如下：\n' "$HEALTH_TIMEOUT" >&2
show_diagnostics
exit 1
