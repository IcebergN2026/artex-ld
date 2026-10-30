# ARTEX 任务执行模式与 Dispatcher 优化——实施记录

状态：已实施并部署。  
最后更新：2026-10-29。  
实施范围：Goal、Planner、vuln_dispatcher 和 finding 触发上下文。  
明确排除：Worker 会话复用已按用户要求全部撤回；没有新增工具硬封禁；历史任务快照不变。

## 1. 实施目标

本次完成此前讨论但尚未落地的三项功能：

1. 优化 Dispatcher 子任务的 `description` 和 `goal`。
2. 为“只验证/复现漏洞，不需要 Shell”的任务提供 Goal/Planner 执行模式。
3. Dispatcher 根据执行模式选择立足点派发或仅漏洞深化派发。

Worker 会话复用不是交付项。相关代码、测试和迁移曾短暂编写，但在部署前已按用户要求全部删除。

## 2. 执行模式定义

实现：`agent/execution_mode.go:19`

```go
type ExecutionMode string

const (
    ExecutionModeFoothold          ExecutionMode = "foothold"
    ExecutionModeVulnerabilityOnly ExecutionMode = "vulnerability_only"
)
```

`ExecutionModeFromGoal(text string)` 只识别明确的仅漏洞意图，不把普通漏洞验证全部改成仅漏洞模式。

| 输入 | 模式 | 原因 |
|---|---|---|
| 对目标进行渗透测试 | foothold | 默认行为不变 |
| 验证 RCE | foothold | 普通验证没有明确声明“只验证” |
| 验证 RCE 并建立反弹 Shell | foothold | 明确 Shell 目标 |
| 只验证漏洞，不需要 getshell | vulnerability_only | 明确仅漏洞意图 |
| 仅复现漏洞，不建立 Shell | vulnerability_only | 明确仅漏洞意图 |
| [执行模式:仅漏洞路径] 验证 RCE | vulnerability_only | 结构化标记 |
| 只验证漏洞但需要getshell | foothold | Shell 目标优先 |

英文标记支持 `vulnerability only`、`vulnerability validation only`、`PoC only`、`no shell required` 等。识别前统一大小写并忽略空格、换行、连字符、下划线。

否定词会先移除再判断强 Shell 意图，避免把“不需要getshell”误判为 Shell 任务。

## 3. Goal 优化

接入：`agent/goals.go:137`

```go
sys += goalExecutionModeSpec(goalText, desc)
```

只有识别为 `vulnerability_only` 时才追加下面的代码持有提示词；普通任务和自定义模板不增加该段。

完整提示词：

```text
**执行模式：仅漏洞路径（已识别，最高优先级语义约束）**
- 本任务只要求确认、复现或解释用户指定漏洞及其边界；不要添加 getshell、WebShell、C2、持久化、内网移动或主机控制目标。
- 按实际漏洞类型设置可核验的完成条件：RCE 需要真实命令/代码执行证据；文件上传需要落地及访问/解析证据；其他漏洞按对应可复现证据验收。
- RCE、文件上传、反序列化或权限提升本身就是本模式的合法最终结果，不得升级为必须建立立足点。
- 只有用户同时明确要求 Shell/立足点时才退出本模式；当前识别结果表示没有这种要求。
```

效果：Goal 不应再把仅漏洞任务拆成必须建立 Shell 的目标。

## 4. Planner 优化

接入：`agent/planner.go:413,425,437,447,449`

任务快照、全局 Agent、默认 Planner 模板和内置默认四条路径均追加：

```go
out + plannerExecutionModeSpec(goal) + artifactSpec(workDir)
```

完整提示词：

```text
**执行模式：仅漏洞路径**
- 本任务只要求漏洞确认、复现、影响与边界证据，不要求 getshell、WebShell、C2、持久化或主机控制。
- 按漏洞本身的成功条件验收：RCE 的真实命令/代码执行、文件上传的落地与访问/解析、反序列化利用链证据等，都可以作为最终完成条件。
- 不把“建立立足点”“拿交互 Shell”“权限维持”写成必要意图；发现可进一步利用时只作为后续建议。
- 只有用户明确改变目标、要求 Shell/立足点时，才重新规划为立足点任务。
```

这解决 Planner 把 RCE 或上传漏洞误判为“必须建立 Shell 才能完成”的问题。

## 5. Finding 触发上下文

实现：`server/scheduler.go:155-160`

```go
func findingTriggerContext(e db.TaskEvent) string {
    mode := agent.ExecutionModeFromGoal(e.TaskGoal)
    return fmt.Sprintf("\n\n【本次由任务发现 finding 触发】\n执行模式: %s\n发现: [%s/%s] %s", ...)
}
```

Dispatcher 不再只收到漏洞分类和摘要，还会收到父任务的 `foothold` 或 `vulnerability_only` 模式。

## 6. Dispatcher 优化

实现：`agent/promptcatalog.go`

### 6.1 description 与 goal 分工

```text
description：只写简短标题，如「内网渗透 · <立足点 host>」「漏洞利用 · <资产>」；不要在 description 里堆目标、约束、执行步骤或证据。
```

`goal` 必须包含：

1. `execution_mode`；仅漏洞路径同时写 `[执行模式:仅漏洞路径]`。
2. 父任务、Finding、vulnclass/severity、摘要和证据引用。
3. 立足点 host、已验证会话标识，或待复现资产/入口/参数。
4. 父任务 goal/allow 中明确列出的完整授权目标；多个目标不得只保留立足点主机。
5. 本子任务需要交付的实测结果；仅漏洞任务不强制 Shell 验收。
6. 按需选择的 WebShell、C2、MCP 或漏洞路径。
7. 被动优先、低噪声和禁止破坏性操作。

没有把 description 变成执行指令载体。

### 6.2 双模式派发

foothold 或模式缺失：

- 只接受 `getshell/webshell/reverse_shell`。
- severity 必须为 critical/high。
- 必须有 WebShell 可连接、交互命令回显或 C2/反弹会话上线证据。
- RCE、文件上传和反序列化不能直接派发。

vulnerability_only：

- 接受 `rce/file_upload/deserialization/privesc/获取目标权限`。
- severity 必须为 critical/high。
- 必须有可复现确认证据。
- 选择 `planner_template_nkey=漏洞利用`。
- 不要求 WebShell、C2 或交互会话。

其他漏洞档不自动派发。资产去重和每次最多一个子任务保持不变。

### 6.3 触发器条件

更新 `config-export/agent_triggers.jsonl` 和正式库 `agent_triggers.finding_message`，将两种模式及各自漏洞档、严重度、证据要求明确写出。

`source_task_ids` 仍只负责只读血缘，不被错误解释为授权继承。

## 7. Worker 会话复用撤回

用户明确要求删除后，已移除：

- `WorkerSessionReuseSpec`
- `[会话]` 事实要求
- session/shell ID 跟踪与复用要求
- `worker_session_reuse_v1` 迁移
- 对应测试文件
- `server/assembly.go` 启动调用

正式库核验：

```text
worker_templates 中 [会话]：0
优先复用已验证会话：0
worker_session_reuse_v1：0
```

Worker 原有 `[投放]`、WebShell 和 MCP 规则保持不变。

## 8. 代码与测试定位

| 测试 | 覆盖 |
|---|---|
| `agent/execution_mode_test.go:8 TestExecutionModeFromGoal` | 默认、明确仅漏洞、英文、结构化标记、Shell 优先和否定词 |
| `agent/execution_mode_test.go:32 TestExecutionModePromptSpecs` | Goal/Planner 只在明确模式追加规则 |
| `agent/execution_mode_test.go:47 TestVulnDispatcherDualModeAndGoalContract` | 双模式及完整 goal 契约 |
| `server/execution_mode_test.go:11 TestFindingTriggerContextIncludesExecutionMode` | Finding 上下文传递执行模式 |
| `server/execution_mode_test.go:35 TestVulnDispatcherConfigExportSupportsDualMode` | 导出触发器与双模式一致 |
| `server/vuln_dispatcher_foothold_test.go` | 默认立足点门槛及配置导出一致 |

全量验证：

```bash
ARTEX_PG_DSN=<独立测试库> go test -p=1 ./...
go vet ./...
git diff --check
```

结果：全部通过。

日志：

```text
/root/artex-execution-mode-go-test.log
/root/artex-execution-mode-go-vet.log
```

## 9. 正式库与部署

数据库备份：

```text
/root/artex-pre-execution-mode-20261029-121916.dump
```

权限：0600。

正式库变更：

- vuln_dispatcher 创建并切换到新提示词版本 `version=6`。
- vuln_dispatcher finding 触发器更新为双模式条件。
- 不修改历史任务快照。
- 不修改 Worker 模板或增加会话迁移。

配置已从正式库重新导出到：

```text
artifacts/config-export/
```

Docker 重建后：

```text
artex=healthy
/api/health={"ok":true,"service":"artex","version":"dev"}
```

日志：

```text
/root/artex-execution-mode-deploy.log
/root/artex-execution-mode-export.log
```

## 10. 边界与未验证项

1. 模式识别是确定性输入分类，但 Goal/Planner/Dispatcher 的最终行为仍由 LLM 遵守提示词，不是硬性工具权限控制。
2. 仅漏洞模式需要用户明确表达；普通“验证 RCE”仍走默认模式，避免擅自改变既有任务语义。
3. 没有真实调用 LLM 或向漏洞目标发送请求。
4. 没有建立 WebShell/C2 会话，也没有进行真实子任务派发。
5. 没有修改历史任务，旧任务不会自动重开。
6. 没有加入命令硬封禁或 Worker 会话复用。
