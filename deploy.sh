#!/usr/bin/env bash
# 一键部署：首次启动自动初始化数据库密码，之后始终复用 .env 与命名卷中的数据。
# 默认构建当前源码，支持 --no-build 严格复用已有镜像，支持 --show-secrets 显式显示凭据。
set -Eeuo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

BUILD=1
SHOW_SECRETS=0
for arg in "$@"; do
  case "$arg" in
    --no-build) BUILD=0 ;;
    --show-secrets) SHOW_SECRETS=1 ;;
    -h|--help)
      echo "用法: $0 [--no-build] [--show-secrets]"
      echo "  --no-build      禁止重新构建镜像，严格复用 ARTEX_TAG 指定的已有镜像"
      echo "  --show-secrets  成功后显示数据库密码（默认不显示任何凭据）"
      exit 0
      ;;
    *) die "未知参数: $arg" ;;
  esac
done

command -v docker >/dev/null 2>&1 || die "未找到 docker，请先安装 Docker。"
docker compose version >/dev/null 2>&1 || die "未找到 docker compose 插件。"
command -v curl >/dev/null 2>&1 || die "未找到 curl。"

# 只读取 Compose 使用的键，不 source .env，避免把部署变量当作 Shell 代码执行。
dotenv_value() {
  local key="$1"
  awk -v key="$key" '
    index($0, key "=") == 1 {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
      exit
    }
  ' .env
}

unquote() {
  local value="$1"
  if [[ ${#value} -ge 2 && "$value" == \"*\" && "$value" == *\" ]]; then
    value="${value:1:${#value}-2}"
  elif [[ ${#value} -ge 2 && "$value" == \'*\' && "$value" == *\' ]]; then
    value="${value:1:${#value}-2}"
  fi
  printf '%s' "$value"
}

validate_existing_env() {
  local pg_password pg_user pg_db
  pg_password="$(unquote "$(dotenv_value POSTGRES_PASSWORD)")"
  pg_user="$(unquote "$(dotenv_value POSTGRES_USER)")"
  pg_db="$(unquote "$(dotenv_value POSTGRES_DB)")"
  pg_user="${pg_user:-artex}"
  pg_db="${pg_db:-artex}"

  case "$pg_password" in
    ""|artex|change-me-please|password|postgres)
      die ".env 中 POSTGRES_PASSWORD 为空或仍是弱默认值。请先设置独立强密码；已有命名卷的真实密码不能被部署脚本擅自替换。"
      ;;
  esac
  # Compose 将密码直接插入 postgres:// DSN，非 URL 安全字符会改变连接串含义。
  [[ "$pg_password" =~ ^[A-Za-z0-9._~-]+$ ]] || die "Docker 部署的 POSTGRES_PASSWORD 只能包含字母、数字、点、下划线、波浪线和连字符，避免 DSN/Compose 转义歧义。"
  [[ "$pg_user" =~ ^[A-Za-z0-9_-]+$ ]] || die "POSTGRES_USER 含 Docker DSN 不支持的字符。"
  [[ "$pg_db" =~ ^[A-Za-z0-9_-]+$ ]] || die "POSTGRES_DB 含 Docker DSN 不支持的字符。"
}

if [ ! -f .env ]; then
  info "首次部署：生成 .env（独立随机数据库密码）"
  command -v openssl >/dev/null 2>&1 || die "未找到 openssl，无法生成随机密码。"
  pg_pass="$(openssl rand -hex 32)"
  umask 077
  cat > .env <<EOF
# 密码仅在首次部署时生成；修改它不会重置已有命名卷的数据库密码。
# Docker DSN 使用 URL 安全字符，避免特殊字符被错误解析。
POSTGRES_PASSWORD=${pg_pass}
POSTGRES_USER=artex
POSTGRES_DB=artex
# 留空即使用最新正式版；指定版本可复现升级/回滚。
ARTEX_TAG=
EOF
  chmod 600 .env
  ok "已生成 .env（权限 600）"
else
  info "复用现有 .env 与数据库命名卷"
  validate_existing_env
  chmod 600 .env
fi

tag="$(unquote "$(dotenv_value ARTEX_TAG)")"
tag="${tag:-local}"
info "镜像标签：${tag}"

# 启动前先展开并校验 Compose 配置，避免容器启动后才暴露 DSN/变量错误。
docker compose config --quiet
info "拉取数据库基础镜像"
docker compose pull postgres

up_args=(-d)
if [ "$BUILD" -eq 1 ]; then
  info "从当前源码构建并启动（tag=${tag}）"
  up_args+=(--build)
else
  info "严格复用已有镜像启动（tag=${tag}，--no-build）"
  up_args+=(--no-build)
fi

if ! docker compose up "${up_args[@]}"; then
  warn "docker compose up 失败，输出最近容器日志："
  docker compose ps >&2 || true
  docker compose logs --no-color --tail 100 postgres artex >&2 || true
  die "部署失败，请根据上方日志处理后重试。"
fi

info "等待后端健康检查和前端首页就绪"
healthy=0
for _ in $(seq 1 36); do
  if curl -fsS --max-time 3 http://127.0.0.1:8787/api/health >/dev/null 2>&1 \
    && curl -fsS --max-time 3 http://127.0.0.1:8787/ >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 5
done

if [ "$healthy" -ne 1 ]; then
  warn "服务未在 180 秒内通过完整健康检查，输出容器状态和最近日志："
  docker compose ps >&2 || true
  docker compose logs --no-color --tail 150 postgres artex >&2 || true
  die "健康检查失败（后端 /api/health 或前端首页 /）。"
fi

ok "部署完成，后端健康检查与前端首页均通过"
echo ""
echo "  访问地址:  http://<服务器IP>:8787/"
echo "  首次使用:  打开页面注册管理员账号，并在「系统设置 → LLM」配置模型。"
echo "  数据目录:  ./data（JWT 密钥 data/jwt.key 由服务自动管理并持久化）"
echo "  数据库:    Docker 命名卷 pgdata"
echo ""
if [ "$SHOW_SECRETS" -eq 1 ]; then
  warn "按 --show-secrets 要求显示数据库密码；请勿将此输出粘贴到公开日志。"
  printf '  POSTGRES_PASSWORD=%s\n' "$(unquote "$(dotenv_value POSTGRES_PASSWORD)")"
else
  echo "  数据库密码保存在 .env（权限 600），默认不显示；如需查看请显式使用 --show-secrets。"
fi
echo ""
echo "停止:  docker compose down        （数据保留）"
echo "更新:  修改 ARTEX_TAG 后重新运行 ./deploy.sh（默认重新构建当前源码）"
