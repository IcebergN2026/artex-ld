# 可发布配置示例（已脱敏）

这是一套**可选导入**的 agent / 提示词 / 触发器 / 工具绑定 / 场景预设，用来复现本二开版的一套编排方式。它不是数据库备份，也不是首次启动的必装步骤。

## 安全边界

- **不含 `llm_profiles.jsonl`，不含任何 LLM API Key、模型端点或 LLM 档绑定**。所有 agent 的 `llm_profile_name` 均为 `null`，导入后跟随你的全局模型配置。
- 不含公司、授权范围、任务模板等实例数据，也不导出 `settings.jsonl`。
- `mcp_servers.jsonl` 仅保留配置结构：所有 MCP **默认禁用**，认证值为 `<REPLACE_ME>`，远程示例主机为 `mcp.example.internal`。如需使用，请先在 UI 改成自己的地址和凭据，再手动启用。
- 发布前已扫描私网地址、本机路径和常见密钥形态；仍建议导入前自行检查一遍。

## 包含什么

`manifest.txt` 列出实际文件与行数。共 15 个导入文件，其中 10 个非空：

| 内容 | 说明 |
|---|---|
| 自定义 agent | `vuln_dispatcher`、`reporter`、`retester`、`trace_cleaner` 的配置示例 |
| 内置 agent 覆盖与当前提示词 | 7 个内置 agent 的运行参数覆盖、11 个 agent 的当前提示词 |
| 提示词变量 | 9 条提示词变量的来源与示例说明 |
| 触发器 | `vuln_dispatcher`、`reporter`、`trace_cleaner` 的触发示例 |
| 工具与可见性 | 56 条工具绑定，以及 MCP / skill 可见性示例 |
| 任务分类 | 5 个场景预设及种子意图、默认执行器等字段 |
| 其余空文件 | 自定义工具、模板、通知、自定义拦截规则等保留为空，便于识别格式 |

**代码首启只会自动播种 `vuln_dispatcher` 和 `reporter`。** `trace_cleaner`、`retester` 是这套配置包提供的可选方案，不应描述成源码内置 agent。导入已有同名配置时会按业务键幂等更新，可能覆盖你的同名修改，务必先备份。

## 导入

先备份数据库和现有配置，再在仓库根目录执行：

```bash
# 二选一：完整连接串
export ARTEX_DB_URL='postgres://artex:你的密码@127.0.0.1:5432/artex?sslmode=disable'
# 或使用标准 PGHOST / PGPORT / PGUSER / PGPASSWORD / PGDATABASE

./start.sh import-config ./config-export
```

依赖 `psql`。导入按文件分别在事务中执行，失败文件会报告错误；业务键幂等，可重复导入。文件后缀虽为 `.jsonl`，实际格式是 **PostgreSQL COPY text**（提示词中的换行等字符带 COPY 转义），请用上述命令导入，不要当严格 JSONL 交给普通解析器。

导入后：

1. 在「系统 → LLM」自行配置模型；本包不会创建或选择 LLM 档。
2. 检查 agent 的工具绑定、触发器和提示词，尤其是 `trace_cleaner` 的清理边界。
3. 按需配置并启用 MCP。worker 提示词中的 godzilla 纪律只有在你提供自己的 godzilla MCP 后才有对应工具可用。
4. 新建任务验证场景预设；模板 / 分类修改只影响之后创建的任务快照。

完整私有迁移（含 LLM 档等 19 类对象）请使用 `./start.sh export-config`，并妥善保管明文密钥；详见 [换环境迁移方案](../docs/ARTEX-换环境迁移方案.md)。
