#!/bin/sh
# ARTEX 守护启动脚本（Linux / macOS / Docker ENTRYPOINT）
#
# 用法：
#   ./start.sh                       前台运行（Ctrl-C 停止）
#   nohup ./start.sh >artex.log 2>&1 &   后台常驻
#   ./start.sh -addr :9000           额外参数原样透传给 artex
#
# ── 运维子命令（执行完即退出，不进入守护循环）──────────────────────────
#   ./start.sh export-config [输出目录]   导出 19 类业务配置为 PostgreSQL COPY text jsonl：
#                                        LLM 档、自建/内置 agent 覆盖、提示词与变量、触发器、
#                                        MCP 与可见性、skill 可见性、工具绑定/自定义工具、
#                                        自建模板/分类、拦截与资产拦截规则、任务模板、
#                                        企业与授权范围、通知渠道。
#                                        ARTEX_EXPORT_SETTINGS=1 时另导出 settings.jsonl（含密钥）。
#                                        MCP 本地程序（stdio 二进制、插件、配置）一并打进
#                                        mcp/<服务器名>/ 并写入 mcp_bundles.tsv；源目录用
#                                        ARTEX_MCP_BUNDLE_DIR 指定，默认 ./data/mcp。
#   ./start.sh import-config <导出目录>   把配置导入本机数据库（按业务键判重，幂等）；
#                                        导出包里有 mcp/ 时按 mcp_bundles.tsv 校验 SHA256
#                                        后装到 $ARTEX_MCP_BUNDLE_DIR/<服务器名>/，
#                                        ARTEX_IMPORT_MCP_BIN=0 可只导配置不带程序。
#   ./start.sh upgrade-prompts           应用 scripts/upgrade-review-flow-prompts.sql
#                                        （存量库提示词/模板升级，seed 首插-only 的补充）
#
#   连接串（二选一）：
#     ARTEX_DB_URL='postgres://artex:PASS@127.0.0.1:5432/artex?sslmode=disable'
#     或标准 PG 环境变量 PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE
#   依赖：psql。Docker 镜像内未装 postgresql-client——请在能连库的主机上执行，
#   或容器内 apt-get install -y postgresql-client 后再用。
#   ⚠ 导出物含 API Key / Webhook 密钥明文，目录已 chmod 700，传输后妥善保管。
#
# ── 守护逻辑说明 ─────────────────────────────────────────────────────
# 默认模式只做一件事：把 artex 跑起来，进程退出后按退出码决定要不要再拉起。
#
#   0      用户正常停止        → 退出循环
#   75     程序请求重启        → 立刻重跑（页面点了"一键更新"或"回滚"）
#   其他   崩溃                → 退避后重跑（1→2→4…最多 60 秒）
#
# 刻意不在守护路径里做下载、SHA256 校验或换装：那些逻辑在 sh 和 bat 上要写两套，
# 而它们恰恰是最不能出错的一环——一旦换上跑不起来的二进制，本脚本会忠实地
# 反复拉起它，用户只能上机器手工救。所以校验/换装全部留在 Go 里（selfupdate 包），
# 由 artex 自己在启动时完成，脚本保持傻瓜化。
# （配置迁移子命令在进守护循环之前就分流退出，不影响上面的可靠性约束。）
set -u

cd "$(dirname "$0")" || exit 1

# ════════════════════════ 配置迁移子命令 ════════════════════════════
# 实现（cmd_export_config / cmd_import_config / cmd_upgrade_prompts 及
# cfg_* / dump / imp 助手）拆在 scripts/artex-config.sh，POSIX sh，这里仅 source。
. ./scripts/artex-config.sh

case "${1:-}" in
	export-config)
		shift; cmd_export_config "$@"; exit 0 ;;
	import-config)
		shift; cmd_import_config "$@"; exit 0 ;;
	upgrade-prompts)
		shift; cmd_upgrade_prompts "$@"; exit 0 ;;
esac

# ════════════════════════ 守护启动（默认路径） ════════════════════════

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] 找不到可执行文件 $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# 转发停止信号给 artex 本体。
#
# Docker 下这是必需的：docker stop 只把 SIGTERM 发给 PID 1（也就是本脚本），
# 不会发给子进程。不转发的话 artex 收不到信号、做不了优雅关闭，10 秒后被 SIGKILL
# 硬杀，正在跑的任务直接断在半路。
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# 信号会打断 wait 并让它返回 >128。此时子进程其实还在做优雅关闭，
	# 必须再 wait 一次才能拿到它真正的退出码。
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] 已停止"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] 正常退出"
			exit 0
			;;
		"$RESTART_CODE")
			# 更新/回滚已就绪：重跑后 artex 会在启动时完成换装（见 selfupdate.Bootstrap）。
			echo "[artex] 请求重启（应用新版本）…"
			delay=1
			;;
		*)
			echo "[artex] 异常退出 (code=$code)，${delay}s 后重启" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
