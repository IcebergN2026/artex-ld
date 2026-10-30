# ARTEX Agent 无范围限制改造实施记录

## 1. 决策与范围

用户明确选择“完全不设范围限制”：Agent 可以依据任务目标和发现的资产、服务、网卡、路由等信息，自行继续探测、验证和利用，不再因为地址不在初始列表而停止。

本次保留的纪律：

- 禁止删改业务数据、DoS、关闭审计、持久化后门。
- 保留漏洞证据门槛、复测证据、清理前人工确认和意图人工审核机制。
- 不主动启动真实扫描，不修改历史任务的目标和提示词快照。
- `goal` 使用 GitHub `origin/master` 的原生提示词，不添加或删除任何内容。

> 风险说明：这是无网络范围闸门的运行模式。Bash/MCP 仍可按 Agent 决策访问可达目标；平台不提供网络 ACL 保证。

## 2. Goal 原生提示词核验

文件：`agent/goals.go`

恢复方式：

```bash
git checkout -- agent/goals.go
git show origin/master:agent/goals.go > /tmp/artex-upstream-goals.go
git diff --exit-code origin/master -- agent/goals.go
```

结果：无差异。两个文件的 SHA-256 均为：

```text
89135e2f4757a9ff92bc91439aa1d52ee466c1cc007729d27076b19cfd248f98
```

数据库迁移的 Agent 清单不包含 `goals`，因此启动迁移也不会重写其当前提示词。

## 3. Planner 修改

### 3.1 通用 Planner

文件：`agent/planner.go`

删除 Planner 的约束块注入：

```go
// 已删除：
sysBody += constraintBlock(ts)
```

新增固定操作纪律：

```text
**操作纪律**：禁止破坏性操作（删改业务数据、DoS、关闭审计、持久化后门）；发现的新资产和方向可继续规划探索。
```

删除“路线多样性永远服从操作约束”的句子。Planner 现在可以针对发现的新入口、新资产和新利用链生成意图。

### 3.2 六类内置 Planner 场景

文件：`agent/planner_domain_templates.go`

六个内置场景均删除操作约束对路线选择的限制。内网场景原文：

```text
只对 goal 或 allow 约束明确列出的地址派注意图；其他地址即使直连可达或属于同一网段，也必须停止并请求人工补充授权。
```

替换为：

```text
**新方向**：网卡、路由、网段、凭据和存活服务都可作为后续探索线索；根据任务目标和已有证据继续规划，不因地址不同而停止。
```

漏洞利用场景删除“授权边界”前置条件，保留最小化 PoC 和证据记录。

## 4. Worker 修改

### 4.1 通用 Worker

文件：`agent/worker.go`

删除 Worker 的约束块注入：

```go
// 已删除：
sysBody += constraintBlock(ts)
```

原提示词：

```text
3. 只在授权范围内操作。系统提示顶部若附【操作约束】，那是最高优先级红线：每条命令/探测执行前先自检，违反即不做。
```

替换为：

```text
3. 禁止破坏性操作（删改业务数据、DoS、关闭审计、持久化后门）；除此之外可执行完成当前意图所需的探测、验证和利用。
```

### 4.2 内网 Worker 场景

文件：`agent/worker_domain_templates.go`

新场景规则：

```text
- 从父任务登记的立足点开始，识别主机、网卡、路由、凭据和可达关系，再决定后续利用。
- 每次横向动作都写清源主机、目标主机、协议和证据来源。
- 发现其他网卡、路由、网段或存活服务时，可依据当前意图继续探测和验证，并记录事实与证据。
```

不再要求“等待人工追加授权”。

## 5. Dispatcher 与派生任务

### 5.1 Dispatcher 提示词

文件：`agent/promptcatalog.go`

删除：

- “操作约束检查（派发前必须执行）”整节。
- “父任务 goal/deny 禁止方向不得派生”的规则。
- 派生 goal 中的授权边界文本。
- “误派=制造未授权横向风险”的表述。
- `spawn_task` 禁止关键词硬拦截的提示。

保留：

- 立足点与漏洞证据门槛。
- 每资产去重和每次最多派生一个任务。
- 场景 nkey 选择。
- 被动优先、OPSEC、扫描/爆破限速。
- 禁止删改数据、DoS、持久化后门。

### 5.2 删除派生代码硬闸门

文件：`server/orchestration.go`

从 `toolSpawnTask` 删除对以下函数的调用，并删除不再使用的实现：

```go
matchForbiddenRule(parentRef, childGoal)
matchForbiddenConstraint(parentRef, childGoal)
forbiddenKeywordOf(line)
```

效果：父任务 goal 中的“禁止某方向”及 `task_constraints` 的 deny 文本不再阻止 Agent 派生子任务。

## 6. 运行时约束注入移除

删除文件：`agent/constraints.go`

该文件唯一的 `constraintBlock` 已不再被 Planner 或 Worker 调用，因此一并移除，避免留下不可达的旧范围逻辑。

原生 `goal` 仍会按上游逻辑产生 `task_constraints`；本次没有修改 `goal`、表结构或历史约束数据。变化是 Planner/Worker 不再把这些记录作为系统级执行边界，Dispatcher 也不再读取它们来限制派生。

## 7. 其他提示词

- `agent/tools_review.go`：人工意图审核不再以“撞授权边界”为驳回原因；仍驳回明显重复和删改业务数据、DoS、关闭审计、持久化后门等危险意图。
- `agent/tools_insert.go`：`add_task_scope` 仅用于覆盖统计，不再描述为授权边界。
- `config-export/agent_prompts_current.jsonl`：同步 auto、pentest、planner、worker、vuln_dispatcher；未修改 goals。

## 8. 数据库迁移

文件：`server/scope_prompt_migration.go`

一次性标记：

```text
unrestricted_agent_prompts_v2=done
```

迁移内容：

1. 刷新所有 `builtin=true` 的 Planner 场景模板。
2. 刷新所有 `builtin=true` 的 Worker 场景模板。
3. 更新 auto、pentest、planner、worker、vuln_dispatcher 的当前提示词。
4. 不更新 goals。
5. 不更新自定义场景模板。
6. 不批量更新 `tasks.planner_prompt_override` 或 `tasks.worker_prompt_override`，历史任务快照保持不变。

## 9. 自动化验证

新增/更新测试：

- `agent/unrestricted_prompt_test.go`
  - 验证 Planner/Worker 不含授权边界和扩界审批文字。
  - 验证破坏性操作纪律仍存在。
  - 验证内网模板允许继续探测和验证。
- `agent/scope_prompt_test.go`
  - 验证 Dispatcher 和内置模板已移除旧范围规则。
- `agent/worker_prompt_test.go`
  - 内网场景断言改为“继续探测和验证”。
- `server/scope_prompt_migration_test.go`
  - 验证内置模板迁移和一次性执行。

最终命令：

```bash
ARTEX_PG_DSN='postgres://postgres:test@127.0.0.1:55433/artex_test?sslmode=disable' go test -p=1 ./...
go vet ./...
git diff --check
git diff --exit-code origin/master -- agent/goals.go
```

结果：全部通过。测试数据库和临时容器已清理。

## 10. 部署与线上核验

部署前备份：

```text
/root/artex-pre-unrestricted-20261029-090804.dump
```

权限：`0600`。

部署：

```bash
docker compose up -d --build artex
```

结果：

- `artex-ld-artex-1`：healthy。
- `artex-ld-postgres-1`：healthy。
- `/api/health`：`{"ok":true,"service":"artex","version":"dev"}`。
- `unrestricted_agent_prompts_v2=done`。
- 当前 auto/pentest/planner/worker/vuln_dispatcher 不含旧授权和扩界文字。
- 内置 Planner/Worker 场景的旧范围规则计数均为 0。

日志：

```text
/root/artex-unrestricted-go-test.log
/root/artex-unrestricted-deploy.log
```

## 11. 未验证事项

- 未启动真实渗透任务。
- 未对任何新地址实施扫描或利用。
- 未进行真实 LLM 端到端行为评估。
- 本次不是网络 ACL 或容器 egress 隔离；它是有意取消应用层范围限制。

此前的《ARTEX-内网任务授权范围修正-实施记录.md》描述的是已废弃的严格范围模式，以本文件为当前实现依据。
