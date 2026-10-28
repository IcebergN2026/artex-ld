#!/usr/bin/env bash
# ARTEX 安装脚本：① 全部 Docker  ② 本地编译运行
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# ── docker 环境检测 / 自动安装 ───────────────────
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "已检测到 docker 与 docker compose"; return
  fi
  warn "未检测到 docker / docker compose"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask '自动安装 Docker? (y/n)' y)" = y ]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER" || true
        ok "Docker 安装完成（用户组变更需重新登录后免 sudo）"
      else
        die "请自行安装 docker 后重试"
      fi ;;
    Darwin) die "macOS 请安装 Docker Desktop：https://www.docker.com/products/docker-desktop/" ;;
    *)      die "请自行安装 docker 后重试" ;;
  esac
}

# ── ① 全部 Docker ───────────────────────────────
install_docker(){
  ensure_docker
  # 凭据、Compose 校验、健康检查和失败诊断统一由 deploy.sh 处理，
  # 避免交互安装与常规更新使用不同的安全默认值。
  bash ./deploy.sh
  ok "Docker 安装完成"
}

# ── ② 本地编译运行 ──────────────────────────────
install_local(){
  echo "数据库安装方式："
  echo "  1) 连接已有 PostgreSQL"
  echo "  2) 用 Docker 起一个 PostgreSQL（需要 docker）"
  case "$(ask '选择' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres 密码（回车随机）' "$(rand)")"
      docker run -d --name artex-pg -p 5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask '数据库地址' 127.0.0.1)"
      DB_PORT="$(ask '端口' 5432)"
      DB_USER="$(ask '账号' artex)"
      DB_PASS="$(ask '密码' '')"
      DB_NAME="$(ask '数据库名' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # 对 Shell 无法安全表示的控制字符直接报错，其余引号/反斜杠按 JSON 转义。
  json_value(){
    local value="$1"
    [[ ! "$value" =~ [[:cntrl:]] ]] || die "数据库配置不能包含控制字符"
    value="${value//\\/\\\\}"
    value="${value//\"/\\\"}"
    printf '%s' "$value"
  }
  [[ "$DB_PORT" =~ ^[0-9]+$ ]] && [ "$DB_PORT" -ge 1 ] && [ "$DB_PORT" -le 65535 ] || die "数据库端口必须是 1-65535 的数字"
  case "$DB_SSL" in disable|require|verify-ca|verify-full) ;; *) die "不支持的 sslmode：$DB_SSL" ;; esac
  DB_HOST_JSON="$(json_value "$DB_HOST")"
  DB_USER_JSON="$(json_value "$DB_USER")"
  DB_PASS_JSON="$(json_value "$DB_PASS")"
  DB_NAME_JSON="$(json_value "$DB_NAME")"
  umask 077
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST_JSON}",
    "port": ${DB_PORT},
    "user": "${DB_USER_JSON}",
    "password": "${DB_PASS_JSON}",
    "dbname": "${DB_NAME_JSON}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  chmod 600 config.json
  ok "已生成 config.json（0600）"

  # go 环境检查
  command -v go >/dev/null 2>&1 || die "未检测到 Go，请先安装 Go（>=1.26）：https://go.dev/dl/"
  ok "Go: $(go version)"

  # 内嵌前端需要 node 出静态产物
  if command -v npm >/dev/null 2>&1; then
    info "构建前端静态产物…"
    ( cd web && npm ci && npm run build:static )
    mkdir -p server/webui/dist
    rm -rf server/webui/dist
    cp -r web/out server/webui/dist
    info "编译内嵌单二进制…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "未检测到 npm：将编译**不内嵌前端**的后端（前端需另跑 npm run dev）"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "编译完成 → ./artex"

  info "启动…（Ctrl-C 退出）"
  ./artex
}

# ── ③ 服务器一键部署（二进制包 + 本机 PostgreSQL）────────────────
# 非交互（幂等，可重复执行=升级）：
#   sudo ./install.sh --server [artex-xxx.tar.gz]
# 包省略时自动取当前目录最新的 artex*.tar.gz。重复执行保留 config.json/data/数据库。
# 注意：本脚本【不创建自启动服务】。已存在 artex.service（历史部署）则照常停/起完成
# 升级；全新部署只装文件，启动方式由用户自选（start.sh 守护 / 自建 systemd 等）。
install_server(){
  local pkg="${1:-}"
  [ "$(id -u)" = "0" ] || die "服务器部署需要 root：sudo ./install.sh --server"
  command -v apt-get >/dev/null 2>&1 || die "本模式只支持 apt 系（Ubuntu/Debian）；其他发行版请按部署手册分步执行"
  command -v systemctl >/dev/null 2>&1 || die "未检测到 systemd"

  # 1. 定位部署包
  if [ -z "$pkg" ]; then
    pkg="$(find . -maxdepth 1 -type f -name 'artex*.tar.gz' -printf '%T@ %p\n' 2>/dev/null | sort -nr | sed -n '1s/^[^ ]* //p')"
    [ -n "$pkg" ] || die "当前目录找不到 artex*.tar.gz 部署包；请把包放到本目录或用参数指定：./install.sh --server /path/artex-xxx.tar.gz"
  fi
  [ -f "$pkg" ] || die "部署包不存在：$pkg"
  info "部署包：$pkg"

  # 2. 系统依赖
  info "安装系统依赖…"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    postgresql postgresql-client curl ca-certificates tar gzip python3 logrotate

  # 3. PostgreSQL 就绪
  systemctl enable --now postgresql
  local i; for i in $(seq 1 30); do pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1 && break; sleep 1; done
  pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1 || die "PostgreSQL 未就绪"
  ok "PostgreSQL 就绪"

  # 4. 库与账号（幂等；密码复用已有 config.json，否则随机生成，不落屏）
  local ARTEX_DIR=/opt/artex db_pass="" db_pass_sql=""
  if [ -f "$ARTEX_DIR/config.json" ]; then
    if ! db_pass="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["database"]["password"])' "$ARTEX_DIR/config.json")" || [ -z "$db_pass" ]; then
      die "无法读取 $ARTEX_DIR/config.json 的 database.password；已中止，绝不会在配置读取失败时重置数据库密码"
    fi
  else
    db_pass="$(python3 -c 'import secrets; print(secrets.token_hex(24))')"
  fi
  # 密码只经环境变量交给 Python，并以 SQL literal 通过 stdin 执行；不出现在 psql argv/历史中。
  db_pass_sql="$(DB_PASS="$db_pass" python3 -c 'import os; p=os.environ["DB_PASS"]; assert "\0" not in p; print("\x27" + p.replace("\x27", "\x27\x27") + "\x27")')"
  runuser -u postgres -- psql -v ON_ERROR_STOP=1 -q <<SQL
DO \$do\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'artex') THEN
    CREATE ROLE artex LOGIN;
  END IF;
END
\$do\$;
ALTER ROLE artex WITH LOGIN PASSWORD ${db_pass_sql};
SQL
  runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='artex'" | grep -q 1 \
    || runuser -u postgres -- createdb -O artex artex
  ok "数据库 artex / 账号 artex 就绪（密码不落屏，见 $ARTEX_DIR/config.json）"

  # 更新前先做一次独立数据库备份；密码只通过环境变量传给 pg_dump。
  local backup_dir="/opt/artex-backups/$(date +%Y%m%d-%H%M%S)"
  install -d -m 700 "$backup_dir"
  if runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='artex'" | grep -q 1; then
    PGPASSWORD="$db_pass" pg_dump -h 127.0.0.1 -p 5432 -U artex -d artex -Fc -f "$backup_dir/artex.dump"
    chmod 600 "$backup_dir/artex.dump"
    ok "数据库已备份到 $backup_dir/artex.dump"
  fi

  # 5. 解包安装（保留 data/config；二进制先备份，备份只留最近 5 份）
  install -d -m 755 "$ARTEX_DIR" "$ARTEX_DIR/data"
  chmod 700 "$ARTEX_DIR/data"
  # 已存在 artex.service = 升级：停服覆盖后重启；任何失败都恢复旧二进制并回拉服务。
  # 不存在 = 全新部署：不创建自启动服务，也不停任何进程。
  local svc_exists=0
  if [ -f /etc/systemd/system/artex.service ] || systemctl cat artex.service >/dev/null 2>&1; then
    svc_exists=1
    systemctl stop artex || die "无法停止 artex.service，已中止部署"
  fi
  local tmp src backup="" skills_backup="" installed=0 completed=0
  tmp="$(mktemp -d)"
  rollback_install(){
    local status=$?
    trap - EXIT
    if [ "$completed" -ne 1 ]; then
      warn "安装未成功（状态 $status），恢复部署前状态…"
      if [ "$installed" -eq 1 ]; then
        if [ -n "$backup" ] && [ -f "$backup" ]; then
          cp -a "$backup" "$ARTEX_DIR/artex"
        else
          rm -f "$ARTEX_DIR/artex"
        fi
        if [ -n "$skills_backup" ] && [ -d "$skills_backup" ]; then
          rm -rf "$ARTEX_DIR/skills"
          cp -a "$skills_backup" "$ARTEX_DIR/skills"
        fi
      fi
      [ "$svc_exists" -eq 1 ] && systemctl start artex 2>/dev/null || true
    fi
    rm -rf "$tmp"
    exit "$status"
  }
  trap rollback_install EXIT

  tar -xzf "$pkg" -C "$tmp"
  # 兼容「包内有单层目录」与「平铺」两种打包方式。
  src="$tmp"
  if [ "$(ls -A "$tmp" | wc -l)" = "1" ] && [ -d "$tmp"/"$(ls "$tmp")" ]; then src="$tmp/$(ls "$tmp")"; fi
  [ -f "$src/artex" ] || die "包内找不到 artex 二进制，包结构不对"
  [ "$(head -c 4 "$src/artex")" = "$(printf '\177ELF')" ] || die "包内 artex 不是 ELF 可执行文件，请先构建含前端的 Linux 二进制"

  if [ -f "$ARTEX_DIR/artex" ]; then
    backup="$ARTEX_DIR/artex.bak-$(date +%Y%m%d-%H%M%S)"
    cp -a "$ARTEX_DIR/artex" "$backup"
  fi
  if [ -d "$ARTEX_DIR/skills" ]; then
    skills_backup="$tmp/skills.before"
    cp -a "$ARTEX_DIR/skills" "$skills_backup"
  fi

  # data/ 和 config.json 永远不属于程序包覆盖范围；从此时起失败会触发完整回滚。
  installed=1
  (cd "$src" && tar --exclude='./data' --exclude='./config.json' -cf - .) | tar -xf - -C "$ARTEX_DIR"
  chmod +x "$ARTEX_DIR/artex"
  for executable in start.sh install.sh; do
    [ ! -f "$ARTEX_DIR/$executable" ] || chmod +x "$ARTEX_DIR/$executable"
  done
  # 备份只留最近 5 份；find 无匹配也返回成功，不会让 pipefail 误中断安装。
  find "$ARTEX_DIR" -maxdepth 1 -type f -name 'artex.bak-*' -printf '%T@ %p\n' \
    | sort -nr | sed -n '6,$s/^[^ ]* //p' | while IFS= read -r old; do
      [ -z "$old" ] || rm -f -- "$old"
    done
  ok "程序安装到 $ARTEX_DIR"

  # 5.1 日志轮转：systemd unit / nohup 都是 append 直写 artex.log，不配轮转会无限增长
  # （debug 级别下尤甚）。copytruncate 适配 append 模式，无需通知进程。
  cat > /etc/logrotate.d/artex <<'LR'
/opt/artex/artex.log {
    daily
    rotate 7
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
}
LR

  # 6. config.json（已存在则不动，避免覆盖现场配置）
  if [ ! -f "$ARTEX_DIR/config.json" ]; then
    cat > "$ARTEX_DIR/config.json" <<JSON
{
  "database": {
    "host": "127.0.0.1",
    "port": 5432,
    "user": "artex",
    "password": "$db_pass",
    "dbname": "artex",
    "sslmode": "disable"
  },
  "skill_dir": "$ARTEX_DIR/skills"
}
JSON
    chmod 600 "$ARTEX_DIR/config.json"
    ok "已生成 config.json（0600）"
  else
    info "沿用已存在的 config.json"
  fi

  # 7. 服务处理：不创建自启动服务。已存在的 artex.service（历史部署）重启生效；
  #    全新部署由用户自选运行方式（start.sh 守护 / 自建 systemd / supervisor）。
  if [ "$svc_exists" = "1" ]; then
    systemctl start artex
    local health_base healthy=0
    health_base="${ARTEX_HEALTH_URL:-http://127.0.0.1:8787}"
    health_base="${health_base%/}"
    for i in $(seq 1 30); do
      if systemctl is-active --quiet artex \
        && curl -fsS --max-time 3 "$health_base/api/health" >/dev/null 2>&1 \
        && curl -fsS --max-time 3 "$health_base/" >/dev/null 2>&1; then
        healthy=1
        break
      fi
      sleep 2
    done
    if [ "$healthy" -ne 1 ]; then
      journalctl -u artex -n 80 --no-pager >&2 || true
      die "artex 未通过 systemd 状态、后端健康和前端首页检查（$health_base）"
    fi
    ok "已重启现有 artex.service，完整健康检查通过（未改动 unit 配置）"
  fi

  ok "部署完成"
  echo "    程序目录: $ARTEX_DIR（配置 $ARTEX_DIR/config.json，0600）"
  echo "    日志    : $ARTEX_DIR/artex.log（已配 logrotate：daily 留 7 份）"
  if [ "$svc_exists" = "1" ]; then
    echo "    Web UI : ${ARTEX_PUBLIC_URL:-http://$(hostname -I 2>/dev/null | awk '{print $1}'):8787}"
    echo "    升级    : 把新包放进来再跑一遍本命令即可（配置/数据保留，旧二进制自动备份留 5 份）"
  else
    echo "    启动    : 本脚本不创建自启动服务。手动运行："
    echo "                cd $ARTEX_DIR && nohup ./start.sh >> $ARTEX_DIR/artex.log 2>&1 &"
    echo "              需要开机自启请自行编写 systemd unit，本脚本不代建"
  fi
  echo "    首次初始化: 打开 /setup 设置管理员密码；LLM 在「系统 → LLM」里配"
  completed=1
  trap - EXIT
  rm -rf "$tmp"
}

echo "=============================="
echo "  artex 安装"
echo "  1) 全部 Docker 安装"
echo "  2) 本地运行（go 编译）"
echo "  3) 服务器一键部署（二进制包+PostgreSQL，不代建自启动服务）"
echo "=============================="
case "${1:-}" in
  --server) shift; install_server "$@" ;;  # 非交互直连
  "") case "$(ask '选择' 3)" in
    1) install_docker ;;
    2) install_local ;;
    3) install_server ;;
    *) die "无效选择" ;;
  esac ;;
  *) die "未知参数：$1（支持 --server [包路径]）" ;;
esac
