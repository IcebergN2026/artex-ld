# ARTEX 换环境迁移方案（agent / MCP / tool / 提示词 / 模板）

> 适用范围：把一台 ARTEX 实例上**用户在平台上创建/修改的配置**整体搬到另一台新部署的实例。
> 不搬运行数据（任务、探索图、漏洞、流量、日志）——那些属于业务数据，换环境重测即可。
>
> 配套入口：`start.sh` 子命令（`export-config` / `import-config` / `upgrade-prompts`），无需额外脚本文件。

---

## 1. 三层迁移模型（先理解再操作）

| 层 | 内容 | 迁移方式 |
|---|---|---|
| **代码种子层** | 内置 agent（planner/worker/review/mainagent…）、内置提示词、内置拦截规则（`[内置]` 前缀）、系统工具、planner 内置模板（内置默认 + 五领域）、首启自定义 agent `vuln_dispatcher` / `reporter` | **不需要搬**。新环境首次启动自动落库（server/assembly.go 与 orchestration 的 seed，`ON CONFLICT DO NOTHING` 幂等；首启自定义 agent 删除后不重建） |
| **用户配置层** | 自定义 agent、agent 提示词改版、触发器、MCP 服务器、工具绑定/开关、自定义工具、自定义 planner 模板、任务分类、任务模板、自定义拦截/资产规则、通知渠道、LLM 配置档 | **本方案的导出/导入脚本** |
| **运行数据层** | tasks / exploration_nodes / findings / traffic / activity … | 不迁移 |

### 文件形式的资产（不在数据库里）

| 资产 | 位置 | 迁移方式 |
|---|---|---|
| 自定义 skill | 安装目录 `skills/` 下的自建目录 | **手动拷贝**整个目录（内置 skill 随发行包） |
| TLS 证书 | `-data` 目录 | 一般重新生成即可，无需迁移 |

---

## 2. 导出（旧环境）

```bash
# 在旧环境（能连到其 PostgreSQL 的机器）执行
export ARTEX_DB_URL='postgres://artex:<密码>@127.0.0.1:5432/artex?sslmode=disable'
cd /opt/artex
./start.sh export-config ./artex-config-export

# 可选：连全局开关表 settings 一起导
ARTEX_EXPORT_SETTINGS=1 ./start.sh export-config ./artex-config-export
```

产物：`artex-config-export/` 目录，一组 `*.jsonl` + `manifest.txt`。文件实际是 **PostgreSQL COPY text**（含转义），应使用 `start.sh import-config` 成对导入，不要直接交给严格 JSONL 解析器。

| 文件 | 表 | 判重键（导入幂等依据） |
|---|---|---|
| llm_profiles.jsonl | llm_profiles（全量运行参数：streaming/max_tokens/session_header_key/6 个 retry_*/priority/pool_exclude；is_default 仅在目标无默认档时带入，不抢已有默认） | name |
| planner_templates_custom.jsonl | planner_templates（仅 builtin=false） | nkey |
| task_categories.jsonl | task_categories（含 planner 模板/审核/资产模式/默认描述及 seed_first_intent/seed_executor 种子意图两列；模板 id 导出为 nkey 导入时重映射） | nkey |
| agents_custom.jsonl | agents（仅 builtin=false） | key |
| agents_builtin_overrides.jsonl | agents（仅 builtin=true 的可调字段：enabled/轮次/web_search/wrapup/trigger 模式/executor_key/绑定的 LLM 档名） | key（UPDATE） |
| agent_prompts_current.jsonl | agent_prompts（每 agent 当前版本） | 文本比对，不同才追加新版本 |
| agent_prompt_vars.jsonl | agent_prompt_vars | (agent_key, var_name) |
| agent_triggers.jsonl | agent_triggers（含 tool_names 工具过滤） | agent_key |
| mcp_servers.jsonl | mcp_servers | name（**upsert**，覆盖代码种的占位行） |
| agent_visibility.jsonl | agent_visibility（agent↔MCP 工具授权） | (agent, mcp, tool) |
| agent_skill_visibility.jsonl | agent_skill_visibility | (agent, skill) |
| tools_bindings.jsonl | tools（系统工具只导 agents/enabled/deferred 绑定） | key（UPDATE） |
| tools_custom.jsonl | tools（仅 system=false） | key |
| intercept_rules_custom.jsonl | intercept_rules（仅非 `[内置]`） | name |
| asset_intercept_rules_custom.jsonl | asset_intercept_rules（仅 builtin=false） | (kind, pattern) |
| task_templates.jsonl | task_templates | nkey |
| notification_channels.jsonl | notification_channels（含 filter/rate_per_min） | name |
| companies.jsonl | companies（资产库·公司） | nkey |
| company_scope.jsonl | company_scope（资产库·授权范围，company 按 nkey 重映射） | (company, domain/net/value) |
| settings.jsonl | settings（可选） | key（upsert） |

> ⚠ **导出物含明文敏感信息**：LLM api_key、MCP env 里的 X-API-Key、通知渠道
> webhook/bot_token。导出目录已 `chmod 700`，传输用 scp/sftp，用完即删。

### 公开脱敏子集

仓库的 [`config-export/`](../config-export/README.md) 是另一套**可选示例配置**：不含 LLM 档和密钥、不含公司 / 授权范围 / 任务模板，MCP 全部禁用且地址、凭据为占位符。它用于分享 agent / 提示词 / 触发器 / 场景编排，不替代上述完整私有迁移。导入前备份；同名配置会按业务键幂等更新。

## 3. 导入（新环境）

```bash
# 1) 新环境先正常安装并启动一次 ARTEX（建库、建表、代码种子落库）
#    若由 systemd 管理可执行：systemctl start artex && systemctl stop artex
#    其他部署方式按各自方式启动一次；导入本身不依赖服务在线

# 2) 拷贝导出目录到新环境，执行导入（每个文件一个事务，失败整体回滚该文件）
export ARTEX_DB_URL='postgres://artex:<新密码>@127.0.0.1:5432/artex?sslmode=disable'
cd /opt/artex
./start.sh import-config ./artex-config-export

# 3) 如有自建 skill：把旧环境 skills/ 下的自建目录拷到新环境 skills/ 下

# 4) 按部署方式重启生效（systemd / start.sh / Docker）
# systemctl restart artex
```

导入顺序由脚本保证（LLM 配置档 → planner 模板 → 任务分类 → agent → 提示词 →
触发器 → MCP → 可见性授权 → 工具 → 规则 → 任务模板 → 通知），外键引用全部按
**业务键（name/nkey/key）重映射**，不依赖旧环境的自增 id。

**幂等**：所有 INSERT 按业务键判重，重复执行不会插重；MCP 与 settings 是
upsert（后导入的覆盖）；提示词只在文本有差异时追加新版本。以下验证结论是历史实施记录，
不是对任意新环境的行数保证：曾验证导出→原库导回前后各表行数一致。

## 4. 已知边界与注意事项

1. **agent 提示词历史版本不搬**：只搬每个 agent 的当前版本（导入后成为新环境的
   一个新版本，旧环境的历史版本记录留在旧库）。
2. **agent_prompt_vars 的 description/example 修改不覆盖**：导入按 (agent,var)
   判重跳过——内置变量新环境已由种子建好。若你改过内置变量的描述并希望带过去，
   需手工 SQL 更新。
3. **task_categories.default_planner_template_id / task_templates.category_id /
   agents.llm_profile_id / agent_visibility.resource_id**：全部按 nkey/name/key
   重映射，引用目标在新环境不存在时该行静默置 NULL 或跳过（agent_visibility 缺
   MCP 时跳过该行）。
4. **MCP 的 env 明文密钥**：ScopeSentry 这类带 X-API-Key 的配置会随导出走；
   若新环境密钥不同，导入后在界面上改，或导入前编辑 `mcp_servers.jsonl`。
5. **代码种子与二开代码本身**：新环境必须使用**同一份二开后的二进制/发行包**
   （迁移功能已内嵌 start.sh，随包发布）。只换环境不换包，配置层才完整成立。
6. **通知渠道 filter/rate_per_min 列**：一并迁移；用旧版脚本导出的文件缺这两列时自动取默认值。
7. **资产测绘结果（assets 表）不迁**：它挂 task_ids/last_seen 是运行数据，换环境重测会重新测绘；迁的是资产库的**公司与授权范围**（companies/company_scope）。
8. **postgres 连接**：脚本用 `psql`，ARTEX_DB_URL 或标准 PG* 环境变量二选一。
   Windows 无 psql 时可在数据库所在主机执行（推荐），或本地装 PostgreSQL 客户端。
9. **导出目录路径不能含单引号**（会断 \\copy 的 SQL 引号，脚本会主动报错拒绝）。

## 5. 快速验证清单（导入后）

```bash
psql "$ARTEX_DB_URL" -Atc "
SELECT 'agents', count(*) FROM agents WHERE NOT builtin;
SELECT 'mcp', count(*) FROM mcp_servers WHERE enabled;
SELECT 'custom_tools', count(*) FROM tools WHERE NOT system;
SELECT 'custom_tpl', count(*) FROM planner_templates WHERE NOT builtin;
SELECT 'categories', count(*) FROM task_categories;"
```

- 界面上抽查：一个自定义 agent 的提示词版本、一个 MCP 的连接测试、一个任务的
  分类默认模板是否正确带出。
- 启动日志无 `[prompts] seed ... 失败` / `[tpl] ... 失败` 即种子层完好。

