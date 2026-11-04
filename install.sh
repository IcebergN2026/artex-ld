#!/usr/bin/env bash
# ARTEX 服务器一键部署（二进制包 + 本机 PostgreSQL）。
#
# 唯一入口（非交互、幂等，可重复执行=升级）：
#   sudo bash install.sh --server [artex-xxx.deploy.tar.gz]
# 包省略时自动取当前目录最新的 artex*.tar.gz。重复执行保留 config.json/data/数据库。
#
# 其他路径请走各自的脚本，本文件不再承载：
#   Docker 部署   → ./deploy.sh
#   源码构建     → ./build.sh
#   本地开发     → ./dev.sh
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

# ── 服务器一键部署（二进制包 + 本机 PostgreSQL）────────────────
# 非交互（幂等，可重复执行=升级）：
#   sudo ./install.sh --server [artex-xxx.tar.gz]
# 包省略时自动取当前目录最新的 artex*.tar.gz。重复执行保留 config.json/data/数据库。
# 注意：本脚本【不创建、不管理任何系统服务】（无 systemd unit、无 enable、无自启）。
# 无论全新部署还是覆盖升级都只装文件；启动/停止/重启由用户自行负责
# （start.sh 守护 / 自建 systemd 等），脚本不再代停代起 artex.service。
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

  # 3. PostgreSQL 就绪（只本次启动，不 enable 开机自启；包管理器默认行为不受本脚本控制）
  systemctl start postgresql
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
  # 不做任何服务管理：不检测/停止/重启 artex.service。若旧二进制仍在运行，
  # 先由用户自行停止（或任由下面 rm + 解包替换；Linux 下解包前删除旧文件可
  # 避免覆盖正在运行的可执行文件报 ETXTBSY）。
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
  # 先删旧二进制再解包：覆盖正在运行的可执行文件会 ETXTBSY 导致安装失败；
  # unlink 后运行中的进程仍持有旧 inode，新文件照常落地（已备份，失败可回滚）。
  rm -f "$ARTEX_DIR/artex"
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

  # 5.1 日志轮转：start.sh / nohup 都是 append 直写 artex.log，不配轮转会无限增长
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

  # 7. 无服务处理：不创建/启用/重启任何系统服务。升级场景下旧进程仍在跑旧版本，
  #    由用户自行重启其运行方式（start.sh 守护 / 自建 systemd / supervisor）生效。

  ok "部署完成"
  echo "    程序目录: $ARTEX_DIR（配置 $ARTEX_DIR/config.json，0600）"
  echo "    日志    : $ARTEX_DIR/artex.log（已配 logrotate：daily 留 7 份）"
  echo "    启动    : 本脚本不创建/管理任何服务。手动运行："
  echo "                cd $ARTEX_DIR && nohup ./start.sh >> $ARTEX_DIR/artex.log 2>&1 &"
  echo "              若是覆盖升级，请先自行停止旧进程再启动新版本"
  echo "              需要开机自启请自行编写 systemd unit，本脚本不代建"
  echo "    首次初始化: 打开 /setup 设置管理员密码；LLM 在「系统 → LLM」里配"
  echo "    场景预设 : 包内自带脱敏编排配置，导入：cd $ARTEX_DIR && ./start.sh import-config ./config-export"
  completed=1
  trap - EXIT
  rm -rf "$tmp"
}

usage(){
  cat >&2 <<'EOF'
用法：sudo bash install.sh --server [artex-xxx.deploy.tar.gz]

其他部署路径：
  Docker 部署   ./deploy.sh
  源码构建     ./build.sh
  本地开发     ./dev.sh
EOF
}

case "${1:-}" in
  --server) shift; install_server "$@" ;;
  -h|--help) usage ;;
  *) usage; die "未知参数：${1:-<空>}（唯一支持 --server）" ;;
esac
