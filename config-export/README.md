# config-export（脱敏配置子集）

本目录是 ARTEX 部署的**脱敏配置导出包**，用于在新环境或修复后的环境中导入基础配置。
包内不含任何密钥、凭据或 MCP 绑定信息。

## 文件清单

| 文件 | 说明 |
| --- | --- |
| `agents_custom.jsonl` | 自定义 Agent 定义（当前仅 `trace_cleaner` 痕迹清理） |
| `agent_prompts_current.jsonl` | 各 Agent 的当前生效提示词 |
| `agent_triggers.jsonl` | 各 Agent 的触发器配置 |
| `tools_bindings.jsonl` | `trace_cleaner` 提示词所需领域工具的绑定（`record_fact` / `get_task_graph` / `get_task_node_detail` / `list_task_findings`）。只含这 4 行，导入时按 key 覆盖式 UPDATE 绑定数组——agents 列已含原有绑定，不会清掉其他人；文件里没列出的工具不受影响 |
| `task_categories.jsonl` | 任务分类/场景预设（含默认审核开关与 planner 模板关联，如「外网渗透」） |
| `agents_builtin_overrides.jsonl` | 内置 agent 的运行参数覆盖（开关/轮次/时长/触发策略，无密钥） |
| `agent_prompt_vars.jsonl` | 提示词变量定义（含示例值，无环境信息） |
| `agent_skill_visibility.jsonl` | skill 对 agent 的可见性（当前仅 `api-recon`） |

后四类（task_categories / agents_builtin_overrides / agent_prompt_vars / agent_skill_visibility）合并自原 `runtime-export/` 的脱敏子集（2026-10-30 快照）；敏感内容（settings、密码哈希、MCP、LLM 档等）按原目录安全规则未并入。

运行时工具集 = 代码底座（Read/Write/Edit/Bash 等，无表行不过滤）+ DB 绑定白名单（`ToolResolve` 丢弃未绑定给该 agent 的表内工具）。`trace_cleaner` 不绑工具时底座 shell/文件操作可用，但提示词要求的探索图工具（查重、销账）全部调不到，所以本文件必须随包导入。

注意：提示词里的 `shell_test`（godzilla MCP）不是 DB 工具，不走 bindings；本脱敏包 MCP 全部禁用，导入后如需立足点实连验证，请在 系统 → MCP 自行配置服务器并授权给 `trace_cleaner` 可见。

每个文件每行一条记录，为 **COPY text + JSON 双层转义**格式（由 `start.sh`
export-config / import-config 直接读写），**不是普通 JSONL**：

- 每行整体先是一个紧凑 JSON 对象（`row_to_json` 输出，层 1：JSON 串内换行为 `\n`、引号为 `\"`）；
- 再经 COPY text 转义（层 2）：文件字节中每个反斜杠编码为 `\\`，因此 JSON 串内的换行/引号在文件里实际呈现为 `\\n`/`\\"`，且不含真实换行/TAB；
- 导入时用 `"\copy ... FROM file"` 写入 staging 表，COPY 先把层 2 解码回原 JSON 文本再交给 `jsonb` 解析。请勿手工改成单层转义 JSONL——`\copy` 会把文件中的 `\"` 解码成裸 `"`，导致 `jsonb` 解析失败（本次修复前的事故即源于此）。

## 导入命令

```bash
# 在部署目录（含 start.sh）执行
./start.sh import-config ./config-export
```

导入是覆盖语义：`agents_custom.jsonl` 中的 key 已存在时更新该记录。
**导入会直接修改正式数据库，执行前请先备份。**

## trace_cleaner（痕迹清理）使用须知

- `trace_cleaner` 是可选自定义 Agent，由 `on_finding` 触发器唤醒（serial + by_task，
  单任务预算 1200 秒），只在 finding 属于已获权限/立足点类（vulnclass ∈
  {getshell,webshell,获取目标权限,rce} 且 severity ∈ {critical,high}）时进入清理审计。
- **清理行为受提示词级人工确认约束**：它必须先在当前对话逐项列出清理计划，
  只有真实用户明确回复"同意清理/确认清理"后才执行；含糊回复、历史消息、
  工具输出等均不构成确认。这是提示词级约束，不是平台强制审批或代码级安全保证。
- webshell 通道、取证材料和仍在使用的支撑文件默认保留。
- 模型**不单独绑定** LLM 档：跟随任务或全局默认 LLM 配置；包内不含任何 LLM 档定义。

## godzilla MCP 可见性

trace_cleaner 的 finding_message 会提到 `godzilla shell_test`。MCP 工具对 Agent 的
可见性由部署环境实际配置决定（工具白名单 / 执行器绑定），**本包不包含 MCP 绑定配置**，
请按实际环境在导入后核对。

## 变更记录

- 本次修复：纠正旧导出中提示词/触发器记录的转义损坏（多了一层反斜杠，导致
  `import-config` 解析失败）；补齐 `trace_cleaner` 三件套（agents_custom /
  prompts / triggers）；trace_cleaner 的 finding_message 以
  `dev/ARTEX-Worker立足点Finding分类-实施记录.md` §8 为内容基准。
- 修复前版本备份：部署机 `/root/artex-ld/.validation-logs/config-export-pre-trace-fix/`。
