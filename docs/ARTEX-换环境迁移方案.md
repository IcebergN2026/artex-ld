# 换环境迁移

把一台实例的**配置**搬到新实例。不搬运行数据（任务、漏洞、流量），那些换环境重测。

## 导出（旧环境）

```bash
export ARTEX_DB_URL='postgres://artex:<密码>@127.0.0.1:5432/artex?sslmode=disable'
cd /opt/artex
./start.sh export-config ./my-export          # 19 类配置
ARTEX_EXPORT_SETTINGS=1 ./start.sh export-config ./my-export   # 可选：+ settings（含密码哈希）
```

> 迁移命令只认 `ARTEX_DB_URL`（或 `PG*` 变量），**不读 config.json**；不设会报错。
> 导出物含明文密钥，chmod 700、scp 传输、用完即删，不进仓库。

## 导入（新环境）

```bash
# 新环境先正常安装并启动过一次（建库建表、种子落库）
export ARTEX_DB_URL='postgres://artex:<新密码>@127.0.0.1:5432/artex?sslmode=disable'
cd /opt/artex
./start.sh import-config ./my-export          # 完整迁移
# 可选：导入仓库附带的编排配置快照（同名配置会覆盖，先备份）
./start.sh import-config ./config-export
# 自建 skill 是文件不是表：手动把旧环境 skills/ 自建目录拷过来
```

导入按业务键幂等，每文件单事务；同名配置覆盖更新；MCP/settings 为 upsert。

## 配置快照 config-export/

系统工具及 `reporter`、`vuln_dispatcher`、`retester`、`trace_cleaner` 已由代码在首启时幂等 seed，**无需导入本目录即可使用**。数据库中的后续修改和删除不会被 seed 恢复。

`./start.sh import-config ./config-export` 仅作为可选的配置快照导入；同名配置会覆盖，操作前先备份。导入前必须检查 `mcp_servers.jsonl`、`mcp/`、LLM 和通知配置：这些内容可能含凭据、程序包、日志或目标数据，不属于内置 Agent 的依赖，也不应作为脱敏材料外发。

两个要点：

1. **工具绑定是白名单**：代码 seed 只建立默认绑定；用户手动删除 Agent 或解绑工具后，需自行恢复对应配置。
2. **MCP 仍需单独配置**：`trace_cleaner` 提示词里的 `shell_test` 来自 Godzilla MCP；需要时在系统 → MCP 配置并授权可见，项目不会内置其连接凭据。

## 验证

```bash
psql "$ARTEX_DB_URL" -Atc "SELECT count(*) FROM agents WHERE NOT builtin;"
curl -fsS http://127.0.0.1:8787/ >/dev/null && echo OK
```
