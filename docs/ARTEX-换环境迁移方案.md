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
# 或只导入仓库脱敏编排包（agent/提示词/触发器/工具绑定/场景分类，无密钥）
./start.sh import-config ./config-export
# 自建 skill 是文件不是表：手动把旧环境 skills/ 自建目录拷过来
```

导入按业务键幂等，每文件单事务；同名配置覆盖更新；MCP/settings 为 upsert。

## 脱敏包 config-export/

`./start.sh import-config ./config-export` 一键导入：trace_cleaner agent + 提示词 +
on_finding 触发器 + 4 个工具绑定 + 任务分类场景。详见 `config-export/README.md`。

两个要点：

1. **工具绑定是白名单**：纯配置 agent 不绑工具则探索图工具（record_fact 等）调不到，
   包内 `tools_bindings.jsonl` 已带好（全量覆盖语义，勿手工删行）。
2. **MCP 不随包**：trace_cleaner 提示词里的 `shell_test` 是 godzilla MCP，需要时在
   系统 → MCP 自行配置并授权可见。

## 验证

```bash
psql "$ARTEX_DB_URL" -Atc "SELECT count(*) FROM agents WHERE NOT builtin;"
curl -fsS http://127.0.0.1:8787/ >/dev/null && echo OK
```
