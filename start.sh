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

CFG_OUT="artex-config-export-$(date +%Y%m%d-%H%M%S)"

cfg_info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
cfg_ok()   { printf '\033[32m[+]\033[0m %s\n' "$*"; }
cfg_die()  { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

cfg_check() {
	command -v psql >/dev/null 2>&1 || cfg_die "未检测到 psql（PostgreSQL 客户端）"
	PSQL_URL=""
	[ -n "${ARTEX_DB_URL:-}" ] && PSQL_URL="$ARTEX_DB_URL"
}

# cfg_sha <文件> —— 输出 SHA256（Linux 用 sha256sum，macOS 用 shasum）
cfg_sha() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

# dump <文件名> <SQL 查询> —— 每行一个 row_to_json
# :'mcp_root' 在 SQL 里可用：导出机的 MCP 程序路径会被换成 {{MCP_ROOT}}（路径为空则原样）
dump() {
	_file="$1"; shift
	cfg_info "导出 $_file"
	psql -X -q -At $PSQL_URL <<EOF > "$CFG_OUT/$_file" || cfg_die "导出 $_file 失败"
\set mcp_root '${MCP_BUNDLE_SRC:-}'
COPY ($*) TO STDOUT
EOF
	printf '    %s 行\n' "$(wc -l < "$CFG_OUT/$_file" | tr -d ' ')"
}

cmd_export_config() {
	CFG_OUT="${1:-$CFG_OUT}"
	cfg_check
	mkdir -p "$CFG_OUT"
	chmod 700 "$CFG_OUT"

	# MCP 本地程序目录：$ARTEX_MCP_BUNDLE_DIR/<服务器名>/ 放该服务器需要的二进制、插件与配置；
	# 库里 command/args 写该目录的绝对路径，导出时换成 {{MCP_ROOT}} 跟着配置包一起走。
	MCP_BUNDLE_SRC="$(cd "${ARTEX_MCP_BUNDLE_DIR:-$PWD/data/mcp}" 2>/dev/null && pwd || true)"
	case "$MCP_BUNDLE_SRC" in *"'"*) cfg_die "MCP 程序目录路径不能含单引号: $MCP_BUNDLE_SRC";; esac

	# LLM 配置档（含 api_key，敏感；重试/流式/会话头等全部运行参数随档走）
	dump llm_profiles.jsonl "
SELECT row_to_json(t) FROM (
  SELECT name, format, base_url, proxy, model, api_key, api_key_hint,
         rate_per_second, rate_per_minute, context_window_k,
         reasoning_effort, thinking_type,
         streaming, max_tokens, max_tokens_field, session_header_key,
         retry_connect_attempts, retry_connect_interval_ms,
         retry_empty_attempts, retry_empty_interval_ms,
         retry_stream_attempts, retry_stream_interval_ms,
         is_default, priority, pool_exclude
  FROM llm_profiles ORDER BY id) t"

	# planner 模板（仅自定义行；内置默认+五领域模板由代码种子重建）
	dump planner_templates_custom.jsonl "
SELECT row_to_json(t) FROM (
  SELECT name, nkey, body, is_default
  FROM planner_templates WHERE NOT builtin ORDER BY id) t"

	# 任务分类（场景预设打包：模板/审核/种子意图随分类走；default_planner_template_id 导出为模板 nkey，导入时重映射）
	dump task_categories.jsonl "
SELECT row_to_json(t) FROM (
  SELECT c.name, c.nkey, c.default_review_enabled, c.asset_mode, c.default_description,
         c.seed_first_intent, c.seed_executor,
         pt.nkey AS default_planner_template_nkey
  FROM task_categories c
  LEFT JOIN planner_templates pt ON pt.id = c.default_planner_template_id
  ORDER BY c.id) t"

	# 自定义 agent（builtin=false；reporter/retester 由代码种子重建，导出也无害——导入按 key 判重跳过）
	dump agents_custom.jsonl "
SELECT row_to_json(t) FROM (
  SELECT a.key, a.name, a.description, a.role, a.enabled,
         a.max_turns, a.run_seconds, a.web_search, a.interactive_shell,
         a.wrapup_prompt, a.wrapup_max_turns,
         a.task_timeout_wrapup_prompt, a.task_timeout_wrapup_max_turns,
         a.trigger_run_mode, a.trigger_merge_mode, a.trigger_max_parallel,
         a.executor_key,
         p.name AS llm_profile_name
  FROM agents a
  LEFT JOIN llm_profiles p ON p.id = a.llm_profile_id
  WHERE NOT a.builtin ORDER BY a.id) t"

	# 内置 agent 的用户改版（启用开关/轮次/绑定的 LLM 档等；只 UPDATE 不新增）
	dump agents_builtin_overrides.jsonl "
SELECT row_to_json(t) FROM (
  SELECT a.key, a.enabled, a.max_turns, a.run_seconds, a.web_search, a.interactive_shell,
         a.wrapup_prompt, a.wrapup_max_turns,
         a.task_timeout_wrapup_prompt, a.task_timeout_wrapup_max_turns,
         a.trigger_run_mode, a.trigger_merge_mode, a.trigger_max_parallel,
         a.executor_key, p.name AS llm_profile_name
  FROM agents a LEFT JOIN llm_profiles p ON p.id = a.llm_profile_id
  WHERE a.builtin ORDER BY a.id) t"

	# 全部 agent 的当前提示词（含内置 agent 的人工改版；按 agent key 关联）
	dump agent_prompts_current.jsonl "
SELECT row_to_json(t) FROM (
  SELECT a.key AS agent_key, p.template_text, p.note, p.updated_by
  FROM agent_prompts p
  JOIN agents a ON a.id = p.agent_id
  WHERE p.id = a.current_prompt_id
  ORDER BY a.id) t"

	# agent 提示词变量目录（内置的会被导入端判重跳过）
	dump agent_prompt_vars.jsonl "
SELECT row_to_json(t) FROM (
  SELECT a.key AS agent_key, v.var_name, v.description, v.example, v.source
  FROM agent_prompt_vars v JOIN agents a ON a.id = v.agent_id
  ORDER BY v.id) t"

	# agent 触发器（表内自带 agent_key 列；tool_names 是工具触发过滤器，必带）
	dump agent_triggers.jsonl "
SELECT row_to_json(t) FROM (
  SELECT agent_key, enabled, interval_sec, on_finding, on_goal_met, on_task_timeout,
         on_tool_call, on_task_create, interval_message, finding_message, goal_message,
         task_timeout_message, tool_call_message, task_create_message, tool_names
  FROM agent_triggers ORDER BY id) t"

	# MCP 服务器（含 env 里的密钥，敏感；导入端按 name upsert）
	# command/args 里的 MCP 程序目录前缀换成 {{MCP_ROOT}}，导入端再还原成目标机路径
	dump mcp_servers.jsonl "
SELECT row_to_json(t) FROM (
  SELECT name, transport,
         CASE WHEN :'mcp_root' <> ''
              THEN replace(COALESCE(command, ''), :'mcp_root', '{{MCP_ROOT}}')
              ELSE COALESCE(command, '') END AS command,
         CASE WHEN :'mcp_root' <> ''
              THEN replace(COALESCE(args::text, 'null'), :'mcp_root', '{{MCP_ROOT}}')::jsonb
              ELSE args END AS args,
         env, url, enabled, insecure
  FROM mcp_servers ORDER BY id) t"

	# MCP 本地程序：目录与文件清单一起打进导出包，导入端按清单校验 SHA256 后落盘
	: > "$CFG_OUT/mcp_bundles.tsv"
	if [ -n "$MCP_BUNDLE_SRC" ] && [ -d "$MCP_BUNDLE_SRC" ]; then
		cfg_info "导出 MCP 本地程序（$MCP_BUNDLE_SRC）"
		for _d in "$MCP_BUNDLE_SRC"/*; do
			[ -d "$_d" ] || continue
			_n="${_d##*/}"
			mkdir -p "$CFG_OUT/mcp/$_n" || cfg_die "创建 mcp/$_n 失败"
			cp -a "$_d"/. "$CFG_OUT/mcp/$_n"/ || cfg_die "复制 MCP 程序 $_n 失败"
			(cd "$_d" && find . -type f -print | sort) | while IFS= read -r _f; do
				_r="${_f#./}"
				printf '%s\t%s\t%s\t%s\t%s\n' "$_n" "$_r" "$(cfg_sha "$_d/$_r")" \
					"$(wc -c < "$_d/$_r" | tr -d ' ')" \
					"$( [ -x "$_d/$_r" ] && echo 1 || echo 0 )" >> "$CFG_OUT/mcp_bundles.tsv"
			done
		done
		printf '    %s 行\n' "$(wc -l < "$CFG_OUT/mcp_bundles.tsv" | tr -d ' ')"
	fi

	# agent 对 MCP 工具/资源的可见性（agent 按 key、MCP 按 name 重映射）
	dump agent_visibility.jsonl "
SELECT row_to_json(t) FROM (
  SELECT a.key AS agent_key, v.resource_kind, m.name AS mcp_name,
         v.mcp_tool_name, v.enabled
  FROM agent_visibility v
  JOIN agents a ON a.id = v.agent_id
  LEFT JOIN mcp_servers m ON (v.resource_kind = 'mcp' AND m.id = v.resource_id)
  ORDER BY v.agent_id) t"

	# agent 对 skill 的可见性
	dump agent_skill_visibility.jsonl "
SELECT row_to_json(t) FROM (
  SELECT a.key AS agent_key, s.skill_name, s.enabled
  FROM agent_skill_visibility s JOIN agents a ON a.id = s.agent_id
  ORDER BY s.agent_id) t"

	# 系统工具的绑定/开关（工具本体由代码注册，只搬 agents 绑定与开关）
	dump tools_bindings.jsonl "
SELECT row_to_json(t) FROM (
  SELECT key, agents, enabled, deferred FROM tools WHERE system ORDER BY key) t"

	# 自定义工具整行
	dump tools_custom.jsonl "
SELECT row_to_json(t) FROM (
  SELECT key, description, schema, agents, enabled, kind, exec, deferred
  FROM tools WHERE NOT system ORDER BY key) t"

	# 全局拦截规则（仅非 [内置] 行；内置 ask 规则由代码种子重建）
	dump intercept_rules_custom.jsonl "
SELECT row_to_json(t) FROM (
  SELECT name, enabled, priority, match_target, match_type, pattern, action,
         message, timeout_enabled, timeout_seconds, timeout_action
  FROM intercept_rules WHERE name NOT LIKE '[内置]%' ORDER BY id) t"

	# 全局资产拦截/允许规则（仅非内置行）
	dump asset_intercept_rules_custom.jsonl "
SELECT row_to_json(t) FROM (
  SELECT enabled, kind, pattern, note
  FROM asset_intercept_rules WHERE NOT builtin ORDER BY id) t"

	# 任务模板（category_id 导出为分类 nkey，导入时重映射）
	dump task_templates.jsonl "
SELECT row_to_json(t) FROM (
  SELECT tt.name, tt.nkey, tt.description, tt.goal, tt.intercept_rules,
         c.nkey AS category_nkey
  FROM task_templates tt
  LEFT JOIN task_categories c ON c.id = tt.category_id
  ORDER BY tt.id) t"

	# 资产库：公司（nkey 判重）
	dump companies.jsonl "
SELECT row_to_json(t) FROM (
  SELECT name, nkey, logo
  FROM companies ORDER BY id) t"

	# 资产库：公司授权范围（company 按 nkey 重映射）
	dump company_scope.jsonl "
SELECT row_to_json(t) FROM (
  SELECT c.nkey AS company_nkey, s.kind, s.domain, s.net, s.value, s.raw, s.reason
  FROM company_scope s JOIN companies c ON c.id = s.company_id
  ORDER BY s.id) t"

	# 通知渠道（config 含 webhook/bot_token 等密钥，敏感；filter/rate_per_min 一并带走）
	dump notification_channels.jsonl "
SELECT row_to_json(t) FROM (
  SELECT name, kind, enabled, config, mode, \"filter\", rate_per_min
  FROM notification_channels ORDER BY id) t"

	# settings 全局开关（可选）
	if [ "${ARTEX_EXPORT_SETTINGS:-0}" = "1" ]; then
		dump settings.jsonl "SELECT row_to_json(t) FROM (SELECT key, value FROM settings ORDER BY key) t"
	fi

	{
		echo "artex 平台配置导出"
		echo "时间: $(date -Iseconds)"
		echo "主机: $(hostname)"
		psql -X -q -At $PSQL_URL -c "SELECT '源库: ' || current_database()" 2>/dev/null || true
		# 结构指纹：导入端排障时一眼看出列漂移（哪些列是这次导出时存在的）
		psql -X -q -At $PSQL_URL -c "
SELECT 'cols[' || table_name || ']: ' || string_agg(column_name, ',' ORDER BY ordinal_position)
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name IN
  ('llm_profiles','agents','agent_triggers','notification_channels','planner_templates',
   'task_categories','mcp_servers','tools','intercept_rules','asset_intercept_rules',
   'task_templates','agent_prompts','agent_prompt_vars','agent_visibility',
   'agent_skill_visibility','settings')
GROUP BY table_name ORDER BY table_name" 2>/dev/null || true
	} > "$CFG_OUT/manifest.txt"

	cfg_ok "导出完成：$CFG_OUT"
	echo "    MCP 本地程序：$( [ -d "$CFG_OUT/mcp" ] && du -sh "$CFG_OUT/mcp" | cut -f1 || echo '无' )"
	echo "    导入：把整个目录拷到新环境后执行  ./start.sh import-config $CFG_OUT"
}

# imp <文件> <导入 SQL（读临时表 _st(doc jsonb)）>
# -1 单事务：该文件要么全部入库要么全部回滚，避免中途失败留半个状态
# SQL 里可用 :'mcp_root' 把 {{MCP_ROOT}} 换成目标机的 MCP 程序目录
imp() {
	_file="$1"; _sql="$2"
	[ -s "$CFG_DIR/$_file" ] || { cfg_info "跳过 $_file（空或不存在）"; return 0; }
	cfg_info "导入 $_file"
	psql -X -q -1 -v ON_ERROR_STOP=1 $PSQL_URL <<SQL || cfg_die "导入 $_file 失败，单文件事务已回滚"
\set mcp_root '${MCP_BUNDLE_DST:-}'
CREATE TEMP TABLE _st(doc jsonb);
\copy _st FROM '$CFG_DIR/$_file'
$_sql
SQL
}

cmd_import_config() {
	CFG_DIR="${1:?用法: ./start.sh import-config <导出目录>}"
	[ -d "$CFG_DIR" ] || cfg_die "目录不存在: $CFG_DIR"
	case "$CFG_DIR" in *"'"*) cfg_die "导出目录路径不能含单引号（会断 \\copy 的 SQL 引号）";; esac
	cfg_check

	# MCP 本地程序的落地目录（与导出端的 ARTEX_MCP_BUNDLE_DIR 对应）
	MCP_BUNDLE_DST="$(mkdir -p "${ARTEX_MCP_BUNDLE_DIR:-$PWD/data/mcp}" && cd "${ARTEX_MCP_BUNDLE_DIR:-$PWD/data/mcp}" && pwd)" || cfg_die "创建 MCP 程序目录失败"
	case "$MCP_BUNDLE_DST" in *"'"*) cfg_die "MCP 程序目录路径不能含单引号: $MCP_BUNDLE_DST";; esac

	# 1. LLM 配置档（agents 绑档靠 name 重映射，须最先导入）
	# is_default 有唯一部分索引 uq_llm_one_default：目标已有默认档时导入档不再抢默认
	imp llm_profiles.jsonl "
INSERT INTO llm_profiles (name, format, base_url, proxy, model, api_key, api_key_hint,
                          rate_per_second, rate_per_minute, context_window_k,
                          reasoning_effort, thinking_type,
                          streaming, max_tokens, max_tokens_field, session_header_key,
                          retry_connect_attempts, retry_connect_interval_ms,
                          retry_empty_attempts, retry_empty_interval_ms,
                          retry_stream_attempts, retry_stream_interval_ms,
                          is_default, priority, pool_exclude)
SELECT doc->>'name', doc->>'format', doc->>'base_url', doc->>'proxy', doc->>'model',
       doc->>'api_key', doc->>'api_key_hint',
       (doc->>'rate_per_second')::int, (doc->>'rate_per_minute')::int,
       (doc->>'context_window_k')::int, doc->>'reasoning_effort', doc->>'thinking_type',
       COALESCE((doc->>'streaming')::bool, true),
       COALESCE((doc->>'max_tokens')::int, 0),
       COALESCE(doc->>'max_tokens_field', ''), COALESCE(doc->>'session_header_key', ''),
       (doc->>'retry_connect_attempts')::int, (doc->>'retry_connect_interval_ms')::int,
       (doc->>'retry_empty_attempts')::int, (doc->>'retry_empty_interval_ms')::int,
       (doc->>'retry_stream_attempts')::int, (doc->>'retry_stream_interval_ms')::int,
       COALESCE((doc->>'is_default')::bool, false)
         AND NOT EXISTS (SELECT 1 FROM llm_profiles d WHERE d.is_default),
       COALESCE((doc->>'priority')::int, 0), COALESCE((doc->>'pool_exclude')::bool, false)
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM llm_profiles p WHERE p.name = _st.doc->>'name');"

	# 2. planner 自定义模板（任务分类引用它，须在分类前导入）
	imp planner_templates_custom.jsonl "
INSERT INTO planner_templates (name, nkey, body, builtin, is_default)
SELECT doc->>'name', doc->>'nkey', doc->>'body', false, COALESCE((doc->>'is_default')::bool, false)
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM planner_templates t WHERE t.nkey = _st.doc->>'nkey');"

	# 3. 任务分类（场景预设打包随分类走；default_planner_template_nkey → id 重映射）
	imp task_categories.jsonl "
INSERT INTO task_categories (name, nkey, default_review_enabled, asset_mode,
                             default_description, seed_first_intent, seed_executor,
                             default_planner_template_id)
SELECT doc->>'name', doc->>'nkey',
       COALESCE((doc->>'default_review_enabled')::bool, false),
       doc->>'asset_mode', doc->>'default_description',
       COALESCE((doc->>'seed_first_intent')::bool, false),
       COALESCE(doc->>'seed_executor',''),
       (SELECT id FROM planner_templates pt WHERE pt.nkey = _st.doc->>'default_planner_template_nkey')
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM task_categories c WHERE c.nkey = _st.doc->>'nkey');"

	# 4. 自定义 agent（llm_profile 按 name 重映射；executor_key 即 MCP name，MCP 在第 8 步 upsert）
	imp agents_custom.jsonl "
INSERT INTO agents (key, name, description, role, enabled, builtin,
                    max_turns, run_seconds, web_search, interactive_shell,
                    wrapup_prompt, wrapup_max_turns,
                    task_timeout_wrapup_prompt, task_timeout_wrapup_max_turns,
                    trigger_run_mode, trigger_merge_mode, trigger_max_parallel,
                    executor_key, llm_profile_id)
SELECT doc->>'key', doc->>'name', doc->>'description', doc->>'role',
       COALESCE((doc->>'enabled')::bool, true), false,
       (doc->>'max_turns')::int, (doc->>'run_seconds')::int,
       COALESCE((doc->>'web_search')::bool, false), COALESCE((doc->>'interactive_shell')::bool, false),
       doc->>'wrapup_prompt', (doc->>'wrapup_max_turns')::int,
       doc->>'task_timeout_wrapup_prompt', (doc->>'task_timeout_wrapup_max_turns')::int,
       doc->>'trigger_run_mode', doc->>'trigger_merge_mode', (doc->>'trigger_max_parallel')::int,
       doc->>'executor_key',
       (SELECT id FROM llm_profiles p WHERE p.name = _st.doc->>'llm_profile_name')
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM agents a WHERE a.key = _st.doc->>'key');"

	# 4.5 内置 agent 的用户改版（只 UPDATE 不新增；NULL 字段不动目标现值）
	imp agents_builtin_overrides.jsonl "
UPDATE agents a SET
  enabled                        = COALESCE((doc->>'enabled')::bool, a.enabled),
  max_turns                      = COALESCE((doc->>'max_turns')::int, a.max_turns),
  run_seconds                    = COALESCE((doc->>'run_seconds')::int, a.run_seconds),
  web_search                     = COALESCE((doc->>'web_search')::bool, a.web_search),
  interactive_shell              = COALESCE((doc->>'interactive_shell')::bool, a.interactive_shell),
  wrapup_prompt                  = COALESCE(doc->>'wrapup_prompt', a.wrapup_prompt),
  wrapup_max_turns               = COALESCE((doc->>'wrapup_max_turns')::int, a.wrapup_max_turns),
  task_timeout_wrapup_prompt     = COALESCE(doc->>'task_timeout_wrapup_prompt', a.task_timeout_wrapup_prompt),
  task_timeout_wrapup_max_turns  = COALESCE((doc->>'task_timeout_wrapup_max_turns')::int, a.task_timeout_wrapup_max_turns),
  trigger_run_mode               = COALESCE(doc->>'trigger_run_mode', a.trigger_run_mode),
  trigger_merge_mode             = COALESCE(doc->>'trigger_merge_mode', a.trigger_merge_mode),
  trigger_max_parallel           = COALESCE((doc->>'trigger_max_parallel')::int, a.trigger_max_parallel),
  executor_key                   = COALESCE(doc->>'executor_key', a.executor_key),
  llm_profile_id                 = COALESCE((SELECT id FROM llm_profiles p WHERE p.name = _st.doc->>'llm_profile_name'), a.llm_profile_id)
FROM _st WHERE a.key = _st.doc->>'key' AND a.builtin;"

	# 5. 全部 agent 的当前提示词（仅当当前版本文本与导出文本不同时才追加新版本——幂等）
	imp agent_prompts_current.jsonl "
WITH src AS (
  SELECT a.id AS agent_id, _st.doc->>'template_text' AS body,
         _st.doc->>'note' AS note, COALESCE(_st.doc->>'updated_by','import') AS who
  FROM _st JOIN agents a ON a.key = _st.doc->>'agent_key'
), need AS (
  SELECT s.* FROM src s
  JOIN agents a2 ON a2.id = s.agent_id
  LEFT JOIN agent_prompts cur ON cur.id = a2.current_prompt_id
  WHERE COALESCE(cur.template_text,'') IS DISTINCT FROM s.body
), ins AS (
  INSERT INTO agent_prompts (agent_id, version, template_text, note, updated_by)
  SELECT n.agent_id,
         COALESCE((SELECT max(version) FROM agent_prompts p WHERE p.agent_id = n.agent_id), 0) + 1,
         n.body, n.note, n.who
  FROM need n RETURNING agent_id, id
)
UPDATE agents a SET current_prompt_id = ins.id FROM ins WHERE a.id = ins.agent_id;"

	# 6. agent 提示词变量目录
	imp agent_prompt_vars.jsonl "
INSERT INTO agent_prompt_vars (agent_id, var_name, description, example, source)
SELECT a.id, doc->>'var_name', doc->>'description', doc->>'example', doc->>'source'
FROM _st JOIN agents a ON a.key = _st.doc->>'agent_key'
WHERE NOT EXISTS (
  SELECT 1 FROM agent_prompt_vars v
  WHERE v.agent_id = a.id AND v.var_name = _st.doc->>'var_name');"

	# 7. agent 触发器（一个 agent 一行，按 agent_key 判重）
	imp agent_triggers.jsonl "
INSERT INTO agent_triggers (agent_key, enabled, interval_sec, on_finding, on_goal_met,
                            on_task_timeout, on_tool_call, on_task_create,
                            interval_message, finding_message, goal_message,
                            task_timeout_message, tool_call_message, task_create_message, tool_names)
SELECT doc->>'agent_key', COALESCE((doc->>'enabled')::bool, true),
       (doc->>'interval_sec')::int, (doc->>'on_finding')::bool, (doc->>'on_goal_met')::bool,
       (doc->>'on_task_timeout')::bool, (doc->>'on_tool_call')::bool, (doc->>'on_task_create')::bool,
       doc->>'interval_message', doc->>'finding_message', doc->>'goal_message',
       doc->>'task_timeout_message', doc->>'tool_call_message', doc->>'task_create_message',
       COALESCE(doc->>'tool_names', '')
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM agent_triggers t WHERE t.agent_key = _st.doc->>'agent_key');"

	# 7.5 MCP 本地程序（可选）：按 mcp_bundles.tsv 校验 SHA256 后装到 $MCP_BUNDLE_DST/<服务器名>/
	#     ARTEX_IMPORT_MCP_BIN=0 可跳过（只搬配置，不动可执行文件）
	if [ -s "$CFG_DIR/mcp_bundles.tsv" ]; then
		if [ "${ARTEX_IMPORT_MCP_BIN:-1}" = "1" ]; then
			cfg_info "安装 MCP 本地程序到 $MCP_BUNDLE_DST"
			_tab=$(printf '\t')
			while IFS="$_tab" read -r _n _r _h _s _x; do
				[ -n "$_n" ] || continue
				case "$_r" in /*|*..*) cfg_die "非法文件路径: $_n/$_r";; esac
				_src="$CFG_DIR/mcp/$_n/$_r"
				_dst="$MCP_BUNDLE_DST/$_n/$_r"
				[ -f "$_src" ] || cfg_die "导出包缺文件: mcp/$_n/$_r"
				mkdir -p "${_dst%/*}" || cfg_die "创建目录失败: ${_dst%/*}"
				cp -p "$_src" "$_dst" || cfg_die "复制失败: $_dst"
				[ "$_x" = "1" ] && chmod +x "$_dst"
				_ah=$(cfg_sha "$_dst")
				[ "$_ah" = "$_h" ] || cfg_die "SHA256 不匹配: $_dst（期望 $_h 实际 $_ah）"
			done < "$CFG_DIR/mcp_bundles.tsv"
			printf '    %s 个文件\n' "$(wc -l < "$CFG_DIR/mcp_bundles.tsv" | tr -d ' ')"
		else
			cfg_info "跳过 MCP 本地程序（ARTEX_IMPORT_MCP_BIN=0）"
		fi
	fi

	# 8. MCP 服务器（按 name upsert：用户配置覆盖代码种的占位行）
	# {{MCP_ROOT}} 换成本机 MCP 程序目录，command/args 里的路径随包搬过来
	imp mcp_servers.jsonl "
INSERT INTO mcp_servers (name, transport, command, args, env, url, enabled, insecure)
SELECT doc->>'name', doc->>'transport',
       replace(COALESCE(doc->>'command',''), '{{MCP_ROOT}}', :'mcp_root'),
       CASE WHEN doc ? 'args'
            THEN replace(doc->>'args', '{{MCP_ROOT}}', :'mcp_root')::jsonb
            ELSE 'null'::jsonb END,
       COALESCE(doc->'env','null'::jsonb),
       doc->>'url', COALESCE((doc->>'enabled')::bool, true), COALESCE((doc->>'insecure')::bool, false)
FROM _st
ON CONFLICT (name) DO UPDATE SET
  transport = EXCLUDED.transport, command = EXCLUDED.command,
  args = EXCLUDED.args, env = EXCLUDED.env, url = EXCLUDED.url,
  enabled = EXCLUDED.enabled, insecure = EXCLUDED.insecure;"

	# 9. agent 对 MCP 工具/资源的可见性（resource_kind 现只有 'mcp'，按 name 重映射；
	#    目标环境缺该 MCP 的行直接跳过）
	imp agent_visibility.jsonl "
INSERT INTO agent_visibility (agent_id, resource_kind, resource_id, mcp_tool_name, enabled)
SELECT a.id, 'mcp', m.id, doc->>'mcp_tool_name', COALESCE((doc->>'enabled')::bool, true)
FROM _st
JOIN agents a ON a.key = _st.doc->>'agent_key'
JOIN mcp_servers m ON m.name = _st.doc->>'mcp_name'
WHERE NOT EXISTS (
  SELECT 1 FROM agent_visibility v
  WHERE v.agent_id = a.id AND v.resource_kind = 'mcp' AND v.resource_id = m.id
    AND v.mcp_tool_name IS NOT DISTINCT FROM _st.doc->>'mcp_tool_name');"

	# 10. agent 对 skill 的可见性
	imp agent_skill_visibility.jsonl "
INSERT INTO agent_skill_visibility (agent_id, skill_name, enabled)
SELECT a.id, doc->>'skill_name', COALESCE((doc->>'enabled')::bool, true)
FROM _st JOIN agents a ON a.key = _st.doc->>'agent_key'
WHERE NOT EXISTS (
  SELECT 1 FROM agent_skill_visibility s
  WHERE s.agent_id = a.id AND s.skill_name = _st.doc->>'skill_name');"

	# 11. 系统工具绑定/开关（工具本体由代码注册，只 UPDATE 绑定）
	imp tools_bindings.jsonl "
UPDATE tools t SET agents = _st.doc->'agents',
                   enabled = COALESCE((_st.doc->>'enabled')::bool, t.enabled),
                   deferred = COALESCE((_st.doc->>'deferred')::bool, t.deferred)
FROM _st WHERE t.key = _st.doc->>'key' AND t.system;"

	# 12. 自定义工具整行
	imp tools_custom.jsonl "
INSERT INTO tools (key, description, schema, agents, enabled, kind, exec, deferred, system)
SELECT doc->>'key', doc->>'description', doc->'schema', doc->'agents',
       COALESCE((doc->>'enabled')::bool, true), doc->>'kind', doc->'exec',
       COALESCE((doc->>'deferred')::bool, false), false
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM tools t WHERE t.key = _st.doc->>'key');"

	# 13. 全局拦截规则（仅非内置行）
	imp intercept_rules_custom.jsonl "
INSERT INTO intercept_rules (name, enabled, priority, match_target, match_type, pattern,
                             action, message, timeout_enabled, timeout_seconds, timeout_action)
SELECT doc->>'name', COALESCE((doc->>'enabled')::bool, true), (doc->>'priority')::int,
       doc->>'match_target', doc->>'match_type', doc->>'pattern', doc->>'action',
       doc->>'message', COALESCE((doc->>'timeout_enabled')::bool, false),
       (doc->>'timeout_seconds')::int, doc->>'timeout_action'
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM intercept_rules r WHERE r.name = _st.doc->>'name');"

	# 14. 全局资产拦截/允许规则（仅非内置行）
	imp asset_intercept_rules_custom.jsonl "
INSERT INTO asset_intercept_rules (enabled, kind, pattern, note, builtin)
SELECT COALESCE((doc->>'enabled')::bool, true), doc->>'kind', doc->>'pattern',
       doc->>'note', false
FROM _st
WHERE NOT EXISTS (
  SELECT 1 FROM asset_intercept_rules r
  WHERE r.kind = _st.doc->>'kind' AND r.pattern = _st.doc->>'pattern');"

	# 15. 任务模板（category 按 nkey 重映射）
	imp task_templates.jsonl "
INSERT INTO task_templates (name, nkey, description, goal, intercept_rules, category_id)
SELECT doc->>'name', doc->>'nkey', doc->>'description', doc->>'goal',
       doc->'intercept_rules',
       (SELECT id FROM task_categories c WHERE c.nkey = _st.doc->>'category_nkey')
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM task_templates t WHERE t.nkey = _st.doc->>'nkey');"

	# 16. 通知渠道（按 name 判重；filter/rate_per_min 一并迁移，旧导出缺这两列时取 NULL/默认）
	imp notification_channels.jsonl "
INSERT INTO notification_channels (name, kind, enabled, config, mode, \"filter\", rate_per_min)
SELECT doc->>'name', doc->>'kind', COALESCE((doc->>'enabled')::bool, true),
       doc->'config', doc->>'mode', COALESCE(doc->'filter', '{}'::jsonb),
       COALESCE((doc->>'rate_per_min')::int, 20)
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM notification_channels n WHERE n.name = _st.doc->>'name');"

	# 16.5 资产库：公司（nkey 判重）
	imp companies.jsonl "
INSERT INTO companies (name, nkey, logo)
SELECT doc->>'name', doc->>'nkey', COALESCE(doc->>'logo', '')
FROM _st
WHERE NOT EXISTS (SELECT 1 FROM companies c WHERE c.nkey = _st.doc->>'nkey');"

	# 16.6 资产库：公司授权范围（company 按 nkey 重映射；三列唯一键合并判重）
	imp company_scope.jsonl "
INSERT INTO company_scope (company_id, kind, domain, net, value, raw, reason)
SELECT c.id, doc->>'kind',
       NULLIF(doc->>'domain', ''), NULLIF(doc->>'net', '')::cidr,
       NULLIF(doc->>'value', ''), doc->>'raw', doc->>'reason'
FROM _st JOIN companies c ON c.nkey = _st.doc->>'company_nkey'
WHERE NOT EXISTS (
  SELECT 1 FROM company_scope s
  WHERE s.company_id = c.id
    AND s.domain IS NOT DISTINCT FROM NULLIF(_st.doc->>'domain', '')
    AND s.net    IS NOT DISTINCT FROM NULLIF(_st.doc->>'net', '')::cidr
    AND s.value  IS NOT DISTINCT FROM NULLIF(_st.doc->>'value', ''));"

	# 17. settings 全局开关（可选，按 key upsert）
	imp settings.jsonl "
INSERT INTO settings (key, value)
SELECT doc->>'key', doc->>'value' FROM _st
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;"

	cfg_ok "导入完成。重启 artex 服务后生效：systemctl restart artex"
	echo "    提示：自定义 skill 是文件不是表——如有自建 skill，请把旧环境 skills/ 下的"
	echo "          自建目录一并拷到新环境安装目录的 skills/ 下。"
}

cmd_upgrade_prompts() {
	cfg_check
	_sql="scripts/upgrade-review-flow-prompts.sql"
	[ -f "$_sql" ] || cfg_die "找不到 $_sql（发行包应自带；若手工精简过目录请补回）"
	cfg_info "应用 $_sql"
	psql -X -q -v ON_ERROR_STOP=1 $PSQL_URL -f "$_sql" || cfg_die "升级 SQL 执行失败"
	cfg_ok "提示词/模板升级完成"
}

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
