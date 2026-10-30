# ARTEX 内网 Planner 低噪声提示词实施记录

状态：已实施、已部署。  
范围：仅 Planner 的“内网渗透”内置模板。  
明确排除：Dispatcher、Worker、触发器、其他 Planner 模板、历史任务提示词快照。

## 1. 需求与原因

用户指出内网 Planner 原提示词设置过死：

```text
域控权限、目标主机交互 shell（whoami/hostname 证据）、
核心数据读取/导出、指定网段覆盖
```

本次移除 `whoami/hostname` 的首要原因是这些命令可能触发告警，而不是仅因为它们证据不足。

最终规则：

- 不预设固定网段、域控权限、数据读取或特定命令作为统一完成条件。
- Planner 不指定 `whoami`、`hostname` 等固定命令。
- Worker 按实际情况选择低噪声、最少必要操作。
- 已有证据可复用时不重复执行可能触发告警的命令。
- WebShell、C2 和其他 MCP 都是可选路径，不是内网渗透前置条件。
- 没有可用 MCP 时，仍可规划其他可行的漏洞利用路径。
- 不实现“仅漏洞路径”的 Dispatcher 识别和触发例外。

## 2. 修改文件与代码定位

| 文件 | 位置 | 变更 |
|---|---|---|
| `agent/planner_domain_templates.go` | `plannerDomainInternalTmpl` 第 2 步 | 替换固定达成判据，新增 OPSEC 和可选执行路径 |
| 同上 | 本领域方向生成侧重 | 开局改为低噪声上下文收集，不指定具体命令 |
| `agent/internal_planner_prompt_test.go` | 新文件 | 验证固定判据移除以及新规则存在 |
| 正式库 `planner_templates` | `name=内网渗透`、`builtin=true` | 精确替换对应两段正文，其余模板不变 |

没有修改：

- `agent/promptcatalog.go` 的 Dispatcher；
- `agent/worker.go`、`agent/worker_domain_templates.go`；
- `agent_triggers`；
- `tasks.planner_prompt_override` 历史快照；
- 数据库表结构。

## 3. 达成判据：修改前后

### 修改前

```text
- **本领域达成判据**：域控权限、目标主机交互 shell（whoami/hostname 证据）、核心数据读取/导出、指定网段覆盖——以 worker 带回的实测证据为准，仅"端口开着/主机可达"不算达成。
```

### 修改后

```text
- **本领域达成判据**：按当前 Goal、意图和 Worker 带回的实测证据验收，不预设固定网段、域控权限、数据读取或特定命令作为统一完成条件；仅"端口开着/主机可达"不能代替当前目标要求的实测证据。
- **OPSEC**：Planner 不指定 whoami、hostname 等固定命令；由 Worker 根据实际场景选择低噪声、最少必要的操作。已有证据能够支持结论时直接复用，不重复执行可能触发告警的命令。
- **执行路径**：Planner 根据任务目标、已有漏洞、事实和可用工具自行选择路径。WebShell、C2 和其他 MCP 都是可选方式，不是内网渗透的前置条件；没有可用 MCP 时，仍可规划其他可行的漏洞利用路径。
```

## 4. 开局方向：修改前后

### 修改前

```text
· 开局典型方向：立足点本机信息收集（网卡/路由/内网网段/当前用户与权限/域环境）——这是后续一切方向的前提，优先派且只派一次；
```

### 修改后

```text
· 开局典型方向：立足点本机低噪声上下文收集（优先复用已有事实与可用通道，按需了解网卡/路由/内网网段、必要的身份权限与域环境）——优先生成一个开放意图，不指定具体命令；
```

Planner 仍然可以提出信息收集方向，但不强制 Worker 使用固定命令。具体低噪声操作由 Worker 根据目标环境和已有证据决定。

## 5. 路径选择规则

新规则把执行方式改为 Planner 自主判断：

| 情况 | Planner 行为 |
|---|---|
| 已有可用 WebShell | 可以选择该路径，但不强制 |
| 已有可用 C2 | 可以选择该路径，但不强制 |
| 有其他 MCP | 根据任务和工具能力判断 |
| 没有 MCP | 继续规划其他可行的漏洞利用路径 |
| 只有端口开放/主机可达 | 不能代替当前 Goal 要求的实测证据 |

本轮没有实现“仅漏洞路径”的 Dispatcher 特殊识别，也没有修改 Dispatcher 触发条件。

## 6. 自动化测试

新增 `agent/internal_planner_prompt_test.go`：

### `TestInternalPlannerLowNoiseAndOptionalMCP`

确认旧内容已移除：

```text
whoami/hostname
目标主机交互 shell
指定网段覆盖
```

确认新内容存在：

```text
按当前 Goal、意图和 Worker 带回的实测证据验收
Planner 不指定 whoami、hostname 等固定命令
选择低噪声、最少必要的操作
WebShell、C2 和其他 MCP 都是可选方式
没有可用 MCP 时，仍可规划其他可行的漏洞利用路径
优先生成一个开放意图，不指定具体命令
```

测试命令：

```bash
go test ./agent
ARTEX_PG_DSN=<独立测试库> go test -p=1 ./...
go vet ./...
git diff --check
```

全部通过。

日志：

```text
/root/artex-internal-planner-go-test.log
/root/artex-internal-planner-go-vet.log
```

测试验证提示词内容与范围，不证明真实 LLM 在所有场景下都会遵守。

## 7. 正式库同步

### 备份

```text
/root/artex-pre-internal-planner-20261029-120009.dump
```

权限为 `0600`。

### 更新方式

使用 `UPDATE ... WHERE name='内网渗透' AND builtin=true` 和原文精确匹配，只替换两段正文。没有覆盖整个模板库。

部署后验证：

```text
模板 ID：4
opsec=true
optional_mcp=true
old_criterion=false
low_noise_direction=true
含新 OPSEC 规则的其他模板数量：0
含新规则的历史任务快照数量：0
```

## 8. 部署

```bash
docker compose up -d --build artex
curl http://127.0.0.1:8787/api/health
```

结果：

```json
{"ok":true,"service":"artex","version":"dev"}
```

部署日志：

```text
/root/artex-internal-planner-deploy.log
```

## 9. 边界

1. 本次仅修改提示词，不是工具层 OPSEC 硬限制。
2. Worker 没有被增加固定执行通道约束。
3. Dispatcher 没有新增“仅漏洞路径”识别或触发例外。
4. 历史任务快照没有修改，新规则用于后续采用更新模板的任务。
5. 未执行真实 LLM、内网目标、WebShell 或 C2 测试。
6. 如需强制禁止高噪声命令，需要另立任务设计工具策略或审批规则，不能依赖本提示词提供硬保证。
