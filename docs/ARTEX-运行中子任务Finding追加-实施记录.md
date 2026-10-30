# 运行中子任务 Finding 追加与 Dispatcher 描述/目标优化 — 实施记录

> 后续更新：直接父子血缘和 running 限制已取消。最新规则是“同一资产已有任何内网任务即追加；终态任务新增开放 Goal 并恢复”。请以 [ARTEX-Finding资产派发与追加-实施记录.md](./ARTEX-Finding资产派发与追加-实施记录.md) 为准，本文保留初版实现历史。

> 状态：已实施、已测试、已部署。
> 范围：同一资产已有运行中的内网渗透子任务时补充 Finding；同时按最新要求调整 `spawn_task.description/goal` 的内容分工。
> 不包含：修改历史任务 Goal、自动唤醒非运行任务、真实 LLM 端到端渗透测试。

## 1. 需求与最终行为

原行为：同一资产已有子任务时，不论子任务是否运行，都只去重并结束。后续 Finding 不会进入已有任务。

现行为：

1. Dispatcher 查询父任务的子任务和当前 Finding。
2. 同资产已有 `running` 内网渗透子任务时，不再 `spawn_task`。
3. 对每条新 Finding 调用 `append_finding_to_task`。
4. 工具把 Finding 名称、分类、严重度、摘要和证据写成任务提示。
5. 现有 `add_task_hint` 路径唤醒该任务 Planner，由 Planner 纳入后续规划。
6. 重试返回 `already_appended`，不重复写入。
7. 排队、暂停或终态任务不追加，也不重复新建。

`description/goal` 同步改为：

- `description`：完整七段执行说明，包含模式、触发依据、起点、授权、完成目标、通道和安全纪律。
- `goal`：最终进攻或交付目标，不重复七段细节。
- 内网渗透目标：获取内网最高权限，在明确授权范围内拓展可控主机，定位管理员、域控等关键主机并横向移动，交付证据。
- 仅漏洞路径：复现并确认漏洞影响和边界，不强制 Shell 或横向。

## 2. 代码变更

### 2.1 新工具

新增 `server/finding_handoff.go`：

```go
func (s *Server) toolAppendFindingToTask() actool.CoreTool
```

参数：

```json
{
  "task_id": "运行中的内网子任务 id",
  "finding_id": 163
}
```

返回：

```json
{"status":"appended","task_id":"15","finding_id":163,"hint_id":456,"audit_id":789}
```

重复调用：

```json
{"status":"already_appended","task_id":"15","finding_id":163,"hint_id":456}
```

工具在 `server/orchestration.go` 的 `orchestrationTools` 注册。

### 2.2 血缘、场景和状态校验

写入前必须同时满足：

```go
if target.CategoryName != "内网渗透" { /* 拒绝 */ }
if target.ParentRef != strconv.FormatInt(*finding.TaskID, 10) { /* 拒绝 */ }
lifecycle := target.lifecycleSnapshot()
if lifecycle.Status != "running" || lifecycle.Paused || lifecycle.Queued {
    /* 拒绝 */
}
```

这防止把 Finding 写入：

- 其他父任务的子任务；
- 非内网渗透任务；
- 已完成、失败、暂停或排队任务。

### 2.3 内容传递与长度边界

`findingHandoffText` 写入：

- 持久去重标记；
- 来源任务和 Finding ID；
- vulnclass、severity、名称；
- 摘要和证据；
- 资产锚点。

长度限制：

```go
名称：500 rune
摘要：4000 rune
证据：8000 rune
```

避免异常长证据无界进入提示节点。

### 2.4 持久去重

标记格式：

```text
[dispatcher_finding_id=163]
```

写入前按目标探索和标记查询：

```sql
SELECT id FROM exploration_nodes
WHERE exploration_id=$1 AND kind='hint'
  AND payload->>'text' LIKE $2
ORDER BY id LIMIT 1;
```

已存在则返回 `already_appended`。查询错误与 `sql.ErrNoRows` 分开处理，数据库故障不会被误判为“可以重复写入”。

### 2.5 复用现有唤醒链

工具不创建 Worker、不伪造意图、不修改历史 Goal：

```go
res, err := s.delegateToTask(ctx, payload, (*agent.ToolSet).AddHintTool)
```

`AddHintTool` 完成任务内的提示写入、资产锚定和 Planner 通知。补充成功后增加 `finding_handoff` 审计记录。

### 2.6 触发上下文携带 Finding ID

`db.TaskEvent` 新增：

```go
FindingID int64 `json:"finding_id"`
```

`db/triggers.go` 的 `NewFindingsSince` 关联规范化 Finding：

```sql
LEFT JOIN findings f ON f.node_id=n.id AND f.task_id=t.id
```

`server/scheduler.go` 的触发消息新增：

```text
finding_id: 163
```

因此 Dispatcher 不再只能靠摘要猜测独立 Finding ID。

### 2.7 工具绑定

`seedVulnDispatcherAgent` 和存量升级路径均幂等绑定：

```text
append_finding_to_task
list_task_constraints
get_task_graph
get_task_node_detail
list_task_findings
spawn_task
```

绑定放在一次性提示词升级 flag 判断之前，避免老数据库因 flag 已完成而永远拿不到新工具。

正式库验证：

```json
["auto", "vuln_dispatcher"]
```

## 3. Dispatcher 提示词变更

### 3.1 同资产去重和补充

关键规则：

```text
已有同资产的内网渗透子任务且状态=running：
不再 spawn_task；
对本次每条新 Finding 调用 append_finding_to_task。
already_appended 表示已处理，不重复追加。
```

非运行状态：

```text
排队、暂停或终态：不新建、不向非 running 任务追加。
```

### 3.2 description 七段结构

```text
1. 执行模式
2. 触发依据
3. 目标与起点
4. 授权目标
5. 完成目标
6. 执行通道
7. 安全纪律
```

触发依据可包含父任务、当前 Finding、同资产相关 Finding、凭据或会话证据。安全纪律继承父任务明确约束，不由 Dispatcher 自行放松。

### 3.3 goal 目标

内网渗透：

```text
从已验证立足点出发，获取内网最高权限；
在 description 明确授权范围内尽可能拓展可控内网主机，
定位管理员、域控或其他关键管理主机并横向移动，
交付可验证证据。
```

授权边界仍保留：可达不等于已授权，不得臆造目标。

## 4. 配置与数据库

同步文件：

- `config-export/agent_prompts_current.jsonl`
- `config-export/tools_bindings.jsonl`
- `artifacts/config-export/`

正式 Dispatcher 提示词升级到版本 8。历史提示词版本保留，没有覆盖旧版本。

部署前备份：

```text
/root/artex-pre-finding-handoff-20261029-131440.dump
```

没有修改既有任务 Goal 或历史任务提示词快照。

## 5. 测试与代码映射

新增 `server/finding_handoff_test.go`：

| 测试 | 验证代码 |
|---|---|
| `TestFindingTriggerContextIncludesFindingID` | `server/scheduler.go:findingTriggerContext` |
| `TestVulnDispatcherHandoffAndGoalContract` | `agent/promptcatalog.go:VulnDispatcherDefaultPrompt` |
| `TestAppendFindingToTaskRunningChild` | `server/finding_handoff.go`、AddHint、审计和 `db.NewFindingsSince` |

集成测试覆盖：

1. 首次追加返回 `appended`。
2. 第二次返回 `already_appended`。
3. 只生成一条 hint。
4. hint 包含 Finding 类型、摘要和证据。
5. 只生成一条 `finding_handoff` 审计。
6. 跨血缘被拒绝。
7. 非内网任务被拒绝。
8. 终态任务被拒绝。
9. Finding 事件携带正确的全局 Finding ID。

既有测试同步更新：

- `agent/execution_mode_test.go`
- `server/vuln_dispatcher_foothold_test.go`

全量验证：

```bash
go test -p=1 ./...
go vet ./...
git diff --check
```

全部通过。日志：

```text
/root/artex-finding-handoff-go-test.log
/root/artex-finding-handoff-go-vet.log
```

## 6. 部署与验证

执行：

```bash
docker compose up -d --build artex
```

容器健康检查通过。日志：

```text
/root/artex-finding-handoff-deploy.log
/root/artex-finding-handoff-export.log
```

正式库确认：

- 当前提示词版本为 8；
- 包含 `append_finding_to_task`；
- 包含七段 description 规则；
- 包含内网最高权限和横向目标；
- 工具绑定包含 `vuln_dispatcher`。

## 7. 未验证与边界

- 未运行真实 LLM 让 Dispatcher 自主决策；已验证工具、提示词契约和数据库链路。
- 未对任何真实目标发起网络操作。
- 当前去重依赖 Dispatcher 串行策略和持久标记；没有为并发自定义 Agent 调用新增数据库唯一索引。
- 非 running 任务不会被强制恢复或改写历史 Goal；如需恢复，应由用户显式操作。
