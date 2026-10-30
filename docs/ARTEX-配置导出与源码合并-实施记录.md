# ARTEX 配置导出与源码合并实施记录

状态：已导出、已脱敏、已合并到源码 `config-export/`。

## 1. 目的

源码包不仅包含 Go/React 代码，还要包含当前 ARTEX 的 Agent、提示词、触发器、工具绑定、场景和模板关联，使另一个环境可以通过 `start.sh import-config` 重建配置。

本次不把 LLM 密钥、MCP 认证值、公司资产或运行数据库带入源码包。

## 2. 实际导出方式

代码入口：`start.sh:65` 的 `cmd_export_config`。

在正式 Docker 环境调用 PostgreSQL 容器内的 `psql`，密码只使用容器已有环境变量，没有写在命令或文档中：

```bash
cd /root/artex-ld
docker compose cp start.sh postgres:/tmp/artex-current-start.sh
docker compose exec -T postgres sh -c '
  PGUSER="$POSTGRES_USER"
  PGPASSWORD="$POSTGRES_PASSWORD"
  PGDATABASE="$POSTGRES_DB"
  sh /tmp/artex-current-start.sh export-config /tmp/artex-current-export
'
docker compose cp postgres:/tmp/artex-current-export/. <临时目录>
```

完整导出先放入权限 0700 的临时目录。该临时目录不是源码包内容，合并完成后已删除。

注意：`start.sh export-config` 是完整迁移工具，会导出 LLM 和 MCP 等敏感配置。因此不能不加检查地把整个导出目录提交到源码。

## 3. 导出代码与对象对应

| 对象 | 导出代码位置 | 导入代码位置 | 合并文件 |
|---|---|---|---|
| 场景及模板 nkey 关联 | `start.sh:97` | `start.sh:318` | `task_categories.jsonl` |
| Worker 自定义模板 | `start.sh:91` | `start.sh:311` | `worker_templates_custom.jsonl` |
| Planner 自定义模板 | `start.sh:86` 附近 | `start.sh:305` | `planner_templates_custom.jsonl` |
| Agent 当前提示词 | `start.sh:134` | `start.sh:372` | `agent_prompts_current.jsonl` |
| Agent 自定义和内置覆盖 | `start.sh:109-130` 附近 | `start.sh:333/353` | `agents_custom.jsonl`、`agents_builtin_overrides.jsonl` |
| 触发器 | `start.sh:147` 附近 | `start.sh:401` | `agent_triggers.jsonl` |
| 工具绑定 | `start.sh:181` | `start.sh:450` | `tools_bindings.jsonl` |
| 自定义工具 | `start.sh:188` 附近 | `start.sh:457` | `tools_custom.jsonl` |
| MCP/skill 可见性 | `start.sh:162-177` 附近 | `start.sh:429/441` | `agent_visibility.jsonl`、`agent_skill_visibility.jsonl` |

`start.sh:10` 的对象数量说明同步修正为 20 类；Worker 自定义模板注释明确“外网默认 + 五场景由代码种子重建”。

## 4. 合并后的配置清单

| 文件 | 行数 | 内容 |
|---|---:|---|
| `agent_prompts_current.jsonl` | 11 | 当前十一个 Agent 提示词 |
| `agent_prompt_vars.jsonl` | 9 | 提示词变量目录 |
| `agents_builtin_overrides.jsonl` | 7 | 内置 Agent 参数覆盖 |
| `agents_custom.jsonl` | 4 | 自定义 Agent |
| `agent_skill_visibility.jsonl` | 3 | skill 可见性 |
| `agent_triggers.jsonl` | 3 | reporter、dispatcher、trace_cleaner 触发器 |
| `agent_visibility.jsonl` | 2 | MCP 可见性 |
| `tools_bindings.jsonl` | 56 | 工具开关、Agent 绑定、延迟加载 |
| `tools_custom.jsonl` | 0 | 当前无自定义工具 |
| `task_categories.jsonl` | 6 | 外网渗透 + 五领域场景 |
| `planner_templates_custom.jsonl` | 0 | 当前无自定义 Planner 模板 |
| `worker_templates_custom.jsonl` | 0 | 当前无自定义 Worker 模板 |
| `mcp_servers.jsonl` | 3 | 已脱敏且禁用的 MCP 示例 |
| `notification_channels.jsonl` | 0 | 当前无通知渠道 |
| `intercept_rules_custom.jsonl` | 0 | 当前无自定义全局拦截规则 |
| `asset_intercept_rules_custom.jsonl` | 0 | 当前无自定义资产拦截规则 |

`config-export/manifest.txt` 已按实际行数重新生成；`config-export/README.md` 的场景数量改为 6。

## 5. 外网渗透场景导出内容

`task_categories.jsonl` 现有六行。外网渗透记录包含：

```json
{
  "name": "外网渗透",
  "nkey": "外网渗透",
  "default_review_enabled": true,
  "default_planner_template_nkey": "外网渗透",
  "default_worker_template_nkey": "外网渗透"
}
```

示例省略了与本说明无关的字段；完整记录以 `config-export/task_categories.jsonl` 为准。

导入时不依赖数字模板 ID，而是按 nkey 找回目标数据库的模板：

```sql
SELECT id FROM planner_templates
WHERE nkey = doc->>'default_planner_template_nkey';

SELECT id FROM worker_templates
WHERE nkey = doc->>'default_worker_template_nkey';
```

五个既有领域场景的 Worker 关联可以为空，表示跟随全局默认；本次没有为了让导出“看起来完整”而覆盖这些既有设置。

## 6. 脱敏处理

### 6.1 排除对象

以下内容没有合并进源码：

- `llm_profiles.jsonl`：含 `api_key`。
- `companies.jsonl`、`company_scope.jsonl`：资产实例数据。
- `task_templates.jsonl`：任务实例模板，本次需求不需要。
- 正式数据库、`.env`、JWT、流量证书和运行日志。

### 6.2 MCP

MCP 保留结构和可见性示例，但执行以下变换：

```python
obj["enabled"] = False
obj["env"] = {
    key: ("<REPLACE_ME>" if value not in (None, "") else "")
    for key, value in obj.get("env", {}).items()
}
# URL 主机替换为 mcp.example.internal，保留端口和路径以说明格式。
```

因此不会带出 MCP `Authorization` 或其他 env 认证值。导入后必须由管理员自行填写并启用。

### 6.3 检查

合并后执行了以下断言：

- 六个场景均存在，外网渗透的两个模板 nkey 都正确。
- 所有 MCP 都是 `enabled=false`。
- 所有 MCP env 非空值都为 `<REPLACE_ME>`。
- 不包含本次使用的登录密码或浏览器访问令牌。
- 不包含 `api_key` 字段。
- `git diff --check` 通过。

## 7. 与源码模板的职责划分

内置模板不靠自定义模板导出文件重复保存：

- Planner 外网默认正文：`agent/planner.go`。
- Planner 五领域正文：`agent/planner_domain_templates.go`。
- Worker 公共正文：`agent/worker.go`。
- Worker 五场景正文：`agent/worker_domain_templates.go`。
- 旧默认名称迁移：`db/scenario_template_migration.go`。
- 启动首插：`server/assembly.go:194` 的 `seedPrompts`。

所以 `planner_templates_custom.jsonl` 和 `worker_templates_custom.jsonl` 为 0 行是正确结果，不代表内置模板丢失。

## 8. 验证

外网场景回归测试：

```text
测试：server/scenario_templates_test.go:9
函数：TestSeedPromptsIncludesExternalPentestScenario
实现：server/assembly.go:194 seedPrompts
场景项：server/assembly.go:249
```

结果：聚焦测试通过，全量 `go test -p=1 ./...` 和 `go vet ./...` 通过。

正式环境：

- `/api/task-categories` 返回外网渗透。
- 默认审核开启。
- Planner/Worker 默认模板 ID 均为 1。
- `/api/health` 正常，ARTEX/PostgreSQL 均 healthy。

日志：

```text
/root/artex-external-scenario-focus-test.log
/root/artex-external-scenario-go-test.log
/root/artex-external-scenario-go-vet.log
/root/artex-external-scenario-deploy.log
/root/artex-current-export.log
```

## 9. 备份和恢复

外网场景部署前备份：

```text
/root/artex-pre-external-scenario-20261029-040702.dump
```

导入本配置包前仍必须先备份目标数据库。配置导入会按业务键幂等更新同名 Agent、提示词、触发器和工具绑定，可能覆盖目标环境的同名修改。

## 10. Windows 源码包

最终压缩包包含：

- 当前完整源代码和未提交修改；
- 上述脱敏 `config-export/`；
- 全部实施记录；
- 测试、skills、配置示例和依赖锁文件。

不包含 `.git`、依赖目录、构建产物、`.env`、数据库和运行凭据。压缩包 SHA-256 保存在 Windows 下载目录同名的 `.sha256` 文件中。
