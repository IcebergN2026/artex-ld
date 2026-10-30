# ARTEX 漏洞派发立足点门槛实施记录

## 1. 需求

RCE 只证明可以执行命令，不代表已经上传并连接 WebShell，也不代表 C2 会话已经上线。`vuln_dispatcher` 不应在 RCE finding 出现时立即创建深度子任务，必须等到可继续操作的立足点已经建立。

## 2. 修改文件

- `agent/promptcatalog.go`：修改 `VulnDispatcherDefaultPrompt`。
- `config-export/agent_prompts_current.jsonl`：同步 Dispatcher 当前提示词。
- `config-export/agent_triggers.jsonl`：同步 finding 触发条件。
- `server/vuln_dispatcher_foothold_test.go`：新增提示词和配置包回归测试。

没有修改 `spawn_task` 工具、任务模板映射、去重规则或数据库表结构。

## 3. 新派发条件

必须同时满足 `severity ∈ {critical,high}` 和以下任一立足点证据：

| vulnclass | 必要证据 | 不足证据 |
|---|---|---|
| `webshell` | 已上传成功，并验证可访问或可连接 | 上传漏洞、上传尝试、理论可上传 |
| `getshell` / `reverse_shell` | 交互命令真实回显，或 C2/反弹会话已上线 | 只有 RCE；仅上传木马但未上线 |

以下分类本身不再触发派发：`rce`、`file_upload`、`deserialization`、`privesc`、`获取目标权限`。如果没有 WebShell/C2/交互会话实证，提示词明确禁止调用 `spawn_task`。

提示词还明确“等待不是后台挂起”：本次不派发就直接结束，后续由新的 `webshell/getshell/reverse_shell` finding 再次触发评估。

## 4. 场景映射调整

- “已拿主机立足点”只接受 WebShell 可连接、命令回显或 C2 上线证据。
- 删除把 `rce/deserialization` 直接当作已控主机的措辞。
- “漏洞利用”场景改为：立足点已经建立，后续主要验证同族 CVE 或武器化路径。
- 稳定 `planner_template_nkey`、同资产去重和每次最多一个子任务保持不变。

## 5. 触发器调整

`config-export/agent_triggers.jsonl` 的 Dispatcher finding_message 改为只评估：

```text
vulnclass ∈ {getshell,webshell,reverse_shell}
```

并增加 WebShell 可连接、交互命令回显或 C2/反弹会话上线要求。这样避免 RCE 先调用一次 Dispatcher，同时保证 WebShell 上传后存在新的触发机会。

注意：`start.sh import-config` 对 `agent_triggers` 使用 `WHERE NOT EXISTS`，已有触发器不会被覆盖。因此正式环境除导入当前提示词外，还对唯一 `vuln_dispatcher` 行做了显式 `UPDATE finding_message`；没有重导入其他 Agent 配置。

## 6. 测试

`server/vuln_dispatcher_foothold_test.go`：

- `TestVulnDispatcherRequiresEstablishedFoothold`：验证 WebShell、C2、RCE 阻断和再次触发措辞。
- `TestVulnDispatcherConfigExportMatchesFootholdGate`：验证配置包提示词与内置默认一致，触发器包含 `webshell` 且不含旧的 RCE 直接派发条件。
- 既有 `TestVulnDispatcherDefaultPromptUsesStableTemplateKeys` 继续通过。

验证结果：

```text
go test ./agent ./server -run 'VulnDispatcher' -count=1  通过
ARTEX_PG_DSN=... go test -p=1 ./...                    全部通过
go vet ./...                                           通过
git diff --check                                       通过
```

## 7. 部署核验

- 备份：`/root/artex-pre-dispatcher-foothold-20261029-100650.dump`。
- `docker compose up -d --build artex` 成功，健康接口正常。
- 正式 DB 当前提示词包含 `vulnclass=webshell` 和“仅证明 RCE、仅上传木马但未上线不算”。
- 正式 DB 触发器包含 `getshell,webshell,reverse_shell`，旧的 `getshell/rce/file_upload` 条件已消失。

未做真实 LLM、WebShell 上传或 C2 上线测试；本次验证的是提示词、配置包、数据库当前配置和全量自动化回归。
