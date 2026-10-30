# ARTEX 内网任务授权范围修正实施记录

状态：已被后续需求取代。当前实现以《ARTEX-Agent无范围限制-实施记录.md》为准；本文仅保留历史变更。

日期：2026-10-29。

## 1. 用户要求与授权口径

用户指出内网渗透任务的目标设置不正确，并明确：

- `192.168.9.0/24` 中只允许测试 `192.168.9.101`。
- 该 C 段其他 IP 全部禁止测试。
- 目标主机还有另一块网卡。
- 尚未授权另一网卡连接的其他 IP/网段，因此只能识别和记录本机网卡、路由等配置，不得扫描、探测或横向。

本次只做代码、配置和页面验证，没有对目标网段发起任何网络探测。

## 2. 调查结果

### 2.1 当前任务状态

数据库中任务 `#7`：

```text
description = 内网渗透 · 192.168.9.101（Struts2 S2-045 root RCE 立足点）
status = done
```

没有其他正在运行的内网任务。本次没有重启或恢复任务 `#7`。

用户表示“还没有打内网”，但数据库历史 goal/intent 已包含“同网段可达性”等文字。因此只能确认当前没有活跃执行，不能仅凭任务状态证明历史上没有相关动作。

### 2.2 错误的直接来源

任务 `#7` 原 allow/deny 约束把“直连可达”当作可扩界条件：

```text
allow: 仅允许从192.168.9.101立足点出发；范围限定该主机及直连可达范围...
deny:  超出该主机及直连可达范围前必须人工审批...
```

问题不是 C 段计算错误，而是授权模型错误：**网络可达性不代表用户授权。**

### 2.3 错误提示词来源

`agent/promptcatalog.go` 的 Dispatcher 旧提示词：

```text
仅以触发漏洞的立足点主机为起点；超出该主机及直连可达范围的扩界，先停下并在结论中标注「需人工审批扩界」。
```

这句话会让 Agent 认为“立足点 + 直连可达范围”无需人工审批。任务 `#7` 的 goal 和 constraints 正是由这条规则派生出来的。

## 3. 当前任务的精确修正

### 3.1 goal 中新增的授权段

```text
【授权边界】仅允许测试 192.168.9.101 及其 22/2001/2002/2003 服务；禁止测试 192.168.9.0/24 中除 192.168.9.101 以外的任何地址。

【双网卡与扩界】只允许在 192.168.9.101 上识别和记录自身网卡、路由和另一网卡配置；另一网卡连接的其他 IP/网段未获授权，禁止扫描、探测或横向，需人工明确追加授权后再操作。
```

同时删除 goal 前言中的旧表述：

```text
容器到宿主/同网段的直连可达性
```

替换为：

```text
仅在 192.168.9.101 本机以最小噪声验证同主机 22/2002/2003 服务的凭据复用，并只读记录自身网卡、路由和另一网卡配置
```

`tasks.goal` 和对应 `explorations.goal` 在同一事务中更新。历史 goal/intent 节点没有重写，避免把历史记录伪装成后来才产生的正确文本；它们均为已完成状态。

### 3.2 最终 allow/deny

任务 `#7` 当前约束：

| ID | 类型 | 内容 |
|---|---|---|
| 6 | allow | 仅允许测试 `192.168.9.101` 及其 `22/2001/2002/2003` 服务；只允许在该主机上识别和记录自身网卡、路由和另一网卡配置。 |
| 7 | deny | 禁止测试 `192.168.9.0/24` 内除 `.101` 以外的任何地址；另一网卡发现的其他 IP/网段同样禁止，除非用户之后明确追加授权。 |
| 8 | deny | 保留原有业务数据、可用性和持久化限制。 |
| 9 | allow | 保留限速、串行和复用既有结论。 |
| 10 | allow | 保留测试产物清理要求。 |

核心 SQL 形式：

```sql
BEGIN;
UPDATE tasks SET goal = $new_goal WHERE id = 7;
UPDATE explorations
SET goal = (SELECT goal FROM tasks WHERE id = 7), updated_at = now()
WHERE id = (SELECT exploration_id FROM tasks WHERE id = 7);
UPDATE task_constraints SET text = $new_allow, updated_at = now() WHERE id = 6;
UPDATE task_constraints SET text = $new_deny,  updated_at = now() WHERE id = 7;
COMMIT;
```

部署前备份：

```text
/root/artex-pre-scope-topology-20261029-054132.dump
```

## 4. Dispatcher 提示词修改

文件：`agent/promptcatalog.go:122`；`config-export/agent_prompts_current.jsonl` 同步。

Before：

```text
仅以触发漏洞的立足点主机为起点；超出该主机及直连可达范围的扩界，先停下并在结论中标注「需人工审批扩界」。
```

After：

```text
只以触发漏洞的立足点主机为起点，并且只测试父任务 goal 或 allow 约束明确列出的 IP/服务；禁止测试任何未明确授权的其他主机，包括直连可达或同网段主机。发现其他网卡、路由或网段时只记录事实并请求人工补充授权，不得据此扫描或横向。
```

这条规则不只影响任务 `#7`，也阻止以后 Dispatcher 再生成同类“直连即可测试”的子任务目标。

## 5. 内网 Planner 提示词修改

文件：`agent/planner_domain_templates.go:33`。

新增完整场景规则：

```text
授权边界：网卡、路由或网段 fact 只说明网络条件，不扩大授权。只对 goal 或 allow 约束明确列出的地址派注意图；其他地址即使直连可达或属于同一网段，也必须停止并请求人工补充授权。另一网卡只允许在已授权立足点上识别和记录本机配置。
```

Planner 不能再仅凭新网卡或路由 fact 生成针对其他主机的探索意图。

## 6. 内网 Worker 提示词修改

文件：`agent/worker_domain_templates.go:15-18`。

新增完整执行规则：

```text
只从父任务登记的立足点和明确授权范围开始。先识别主机、网卡、路由、凭据和可达关系，再决定是否利用。
每次横向动作都写清源主机、目标主机、协议和证据来源；直连可达不等于获得授权。
只测试 goal 或 allow 约束明确列出的地址。发现其他网卡、路由或网段时只记录本机配置与证据，不扫描或测试其他地址，等待人工明确追加授权。
```

Worker 仍复用公共骨架中的操作约束、证据和 finding 规则；新增场景段不会替代这些公共规则。

## 7. 一次性数据库迁移

新增 `server/scope_prompt_migration.go`。

### 7.1 为什么需要迁移

只改 Go 常量不会自动改变数据库中已经存在的当前提示词。直接每次启动覆盖又会覆盖用户后续编辑，因此使用一次性设置键：

```go
const scopeAuthorizationPromptMigrationKey = "scope_authorization_prompts_v1"
```

入口位于 `server/assembly.go:240`：

```go
if err := upgradeScopeAuthorizationPrompts(pg); err != nil {
    log.Printf("[prompts] 授权范围提示词迁移失败: %v", err)
}
```

### 7.2 迁移行为

`server/scope_prompt_migration.go:15`：

1. 检查 `settings.scope_authorization_prompts_v1`；已存在则直接返回。
2. 对当前 `vuln_dispatcher` 提示词执行精确旧句替换。
3. 更新 `builtin=true AND nkey='内网渗透'` 的 Planner 模板。
4. 更新同条件的 Worker 模板。
5. 成功后写入 `scope_authorization_prompts_v1=done`。

Dispatcher 实际更新的是版本表和当前指针，而不是不存在的物理表 `agent_prompts_current`：

```sql
UPDATE agent_prompts p
SET template_text = replace(p.template_text, $1, $2), updated_by = 'system'
FROM agents a
WHERE p.agent_id = a.id
  AND a.current_prompt_id = p.id
  AND a.key = 'vuln_dispatcher'
  AND strpos(p.template_text, $1) > 0;
```

自定义 `builtin=false` 模板不被迁移覆盖。迁移完成后的人工修改也不会在重启时再次覆盖。

部署后数据库确认：

```text
scope_authorization_prompts_v1 = done
planner_scope = true
worker_scope = true
dispatcher 包含“禁止测试任何未明确授权的其他主机”
```

## 8. 新增和修改文件

| 文件 | 变更 |
|---|---|
| `agent/promptcatalog.go` | Dispatcher 授权提示词。 |
| `agent/planner_domain_templates.go` | 内网 Planner 场景授权段。 |
| `agent/worker_domain_templates.go` | 内网 Worker 场景授权段。 |
| `agent/scope_prompt_test.go` | 新增防回归测试。 |
| `server/scope_prompt_migration.go` | 新增一次性迁移。 |
| `server/scope_prompt_migration_test.go` | 新增真实 PostgreSQL 迁移测试。 |
| `server/assembly.go` | 接入迁移。 |
| `config-export/agent_prompts_current.jsonl` | 同步可导入的 Dispatcher 提示词。 |

## 9. 测试与实现逐行对应

| 测试 | 测试位置 | 对应实现 | 覆盖点 |
|---|---|---|---|
| `TestScopePromptsDoNotTreatReachabilityAsAuthorization` | `agent/scope_prompt_test.go:8` | `agent/promptcatalog.go:122` | 旧“直连范围可扩界”句子不存在，新禁止规则存在。 |
| 同上 | `agent/scope_prompt_test.go` | `agent/planner_domain_templates.go:33` | Planner 只对明确授权地址派注意图。 |
| 同上 | `agent/scope_prompt_test.go` | `agent/worker_domain_templates.go:18` | Worker 不扫描或测试其他地址。 |
| `TestUpgradeScopeAuthorizationPrompts` | `server/scope_prompt_migration_test.go:10` | `server/scope_prompt_migration.go:15` | 真实 PostgreSQL 中 Planner/Worker 正文被更新。 |
| 同上测试后半段 | `server/scope_prompt_migration_test.go:93` | 一次性 setting 检查 | 迁移后人工编辑不会被第二次执行覆盖。 |
| 全量回归 | `/root/artex-final-fix-go-test.log` | 全部 Go 包 | `ARTEX_PG_DSN` 下 `go test -p=1 ./...` 通过。 |
| 静态检查 | `/root/artex-final-fix-go-vet.log` | 全部 Go 包 | `go vet ./...` 通过。 |
| 前端构建 | `/root/artex-final-fix-web-build.log` | 实际部署前端 | TypeScript 和静态构建通过。 |

## 10. 部署和运行边界

执行：

```bash
docker compose up -d --build artex
```

部署日志：`/root/artex-final-fix-deploy.log`。容器 healthy，`/api/health` 返回 ok。

任务 `#7` 最终仍为 `done`，`old_direct=false`，新授权段存在。没有运行任务、扫描网段或验证另一网卡目标。

提示词和 allow/deny 是应用工作流约束，不是网络防火墙。若需要即使模型犯错也无法访问其他地址，还应在执行环境增加网络级 egress  ACL；本次没有声称已经完成该层隔离。

## 11. 回滚

1. 先停止相关任务，避免恢复旧目标后继续执行。
2. 恢复备份 `/root/artex-pre-scope-topology-20261029-054132.dump`，或按备份反向恢复任务 `#7` 的 goal/constraints。
3. 回滚上述 Go 文件并重建 Docker。
4. 如只需撤销数据库提示词，恢复当前 Dispatcher 版本和内网 Planner/Worker 模板正文；同时删除 `scope_authorization_prompts_v1` 才会允许迁移再次运行。
5. 不要把“删除迁移标记”当作日常回滚手段；它会再次执行覆盖逻辑，必须先恢复匹配的旧代码。
