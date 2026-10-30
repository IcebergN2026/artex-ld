# Finding 资产派发与追加实施记录

> 状态：已实施并部署。本文记录“按 Finding 资产全局去重”和“已有内网任务时跨血缘追加并恢复任务”的最终行为，取代早期仅直接子任务、仅 running 可追加的规则。

## 1. 最终规则

1. 按 Finding 实际关联的 IP/域名去重；端口、URL 路径和服务名不制造新主机。
2. 当前任务已经负责该资产：不新建、不追加。
3. 其他内网任务已经负责该资产：把当前 Finding 追加到那个任务，不要求直接父子血缘，也不要求 running。
4. 已有任务处于排队、暂停或终态：仍不新建替代任务。
5. 终态且没有开放 Goal：追加一个带 Finding 标记的开放 Goal，再通过统一 admission 恢复任务。
6. 只有没有任何同资产内网任务时才允许 `spawn_task`。
7. 数据库唯一约束同时阻止跨父任务和并发重复建任务。

## 2. 资产规范化与唯一约束

### `db/dispatch_targets.go`

- `CanonicalAssetKey(a db.Asset)`：
  - IP → `ip:<地址>`；
  - 域名/URL → 解析 host 后生成 `domain:<主机>`；
  - 服务、端口、路径、用户名不作为独立主机键。
- `CanonicalAssetKeys` 去重并排序。
- `TaskDispatchConflictError` 返回已经负责该资产的任务 ID。
- `insertTaskDispatchTargets` 在创建任务的数据库事务内写入目标。

### `db/schema.sql`

```sql
CREATE TABLE IF NOT EXISTS task_dispatch_targets (
  id BIGSERIAL PRIMARY KEY,
  task_id BIGINT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
  finding_id BIGINT REFERENCES findings(id) ON DELETE SET NULL,
  asset_key TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### `db/tasks.go`

`TaskCreateOptions` 新增：

```go
DispatchAssetKeys []string
DispatchFindingID int64
```

目标记录与任务同事务写入；唯一冲突时整个创建回滚，不会出现重复任务。

## 3. 新建派发

### `server/orchestration.go`

内网 `spawn_task` 必须提供 `finding_id`；多主机 Finding 必须提供 `target_asset`。服务端重新从 Finding 和资产表解析目标键，不信任 Agent 自由填写。

### `server/dispatch_assets.go`

- `findingDispatchAssetKeys` 校验目标资产确实属于 Finding。
- `check_internal_asset_task` 返回当前任务或其他任务是否已负责资产。
- `backfillInternalDispatchTargets` 按最老任务优先，从历史 `finding #N` 和 `finding_id=N` 回填目标；后续重复任务不会抢占记录。

## 4. 同资产 Finding 追加

### `server/finding_handoff.go`

工具：

```text
append_finding_to_task(
  task_id=<已有内网任务>,
  finding_id=<全局 Finding id>,
  target_asset=<多主机 Finding 的可选精确资产>
)
```

校验：

- 目标必须是内网渗透任务；
- `task_dispatch_targets` 必须证明目标任务负责同一资产；
- 不再检查 `ParentRef`，允许祖先、兄弟和其他父任务创建的内网任务；
- 不再要求目标任务 running。

追加内容包含：

- 全局 Finding ID；
- 来源任务；
- vulnclass、severity、名称和摘要；
- 原始证据；
- 关联资产 ID。

持久标记：

```text
[dispatcher_finding_id=N]
```

同一 Finding 对同一任务重复调用返回 `already_appended`，不会重复写 hint 或审计。

## 5. 非运行任务处理

运行中任务继续使用 `add_task_hint` 和 Planner 唤醒。

终态任务不能只写 hint：引擎的 goalless 分支会在没有开放 Goal 时直接结束。因此工具会：

1. 检查是否有开放 Goal。
2. 没有时新增或重新打开带 `[dispatcher_finding_id=N]` 的 Goal。
3. 将 Goal 关联到任务 origin fact。
4. 调用统一 `admitTask(target, "resume")`，保留并发排队规则。
5. 写入 Finding hint 和 `finding_handoff` 审计。

没有直接创建 Worker 意图，因此仍由 Planner 规划并遵守 Review。

## 6. Dispatcher 提示词

`agent/promptcatalog.go` 当前规则：

```text
查询返回 exists=true 且是其他内网任务：不再 spawn_task，
直接把当前 Finding 补充到已有的对应内网渗透任务。
不要求是当前任务的直接子任务，也不要求 running。
已有任务处于排队、暂停或终态时仍追加，不新建替代任务。
```

正式库已切换到 Dispatcher 提示词 **version 13**。历史提示词版本和历史任务快照保留。

## 7. 配置导出

已同步：

- `config-export/agent_prompts_current.jsonl`
- `config-export/tools_bindings.jsonl`
- `artifacts/config-export/agent_prompts_current.jsonl`
- `artifacts/config-export/tools_bindings.jsonl`

绑定包含：

```text
check_internal_asset_task
append_finding_to_task
spawn_task
```

## 8. 测试映射

| 测试 | 文件 | 验证内容 |
|---|---|---|
| `TestCanonicalAssetKeys` | `db/dispatch_targets_test.go` | IP、域名、端口、URL 规范化 |
| `TestTaskDispatchTargetUniqueness` | `db/dispatch_targets_test.go` | 顺序和 6 路并发唯一约束 |
| `TestCheckInternalAssetTaskUsesFindingAssets` | `server/dispatch_assets_test.go` | 多主机选择和资产查询 |
| `TestBackfillInternalDispatchTargets` | `server/dispatch_assets_test.go` | 旧任务最老优先回填 |
| `TestAppendFindingToTaskSameAsset` | `server/finding_handoff_test.go` | 跨血缘追加、幂等、证据、审计、终态 Goal 和复活 |
| `TestFindingTriggerContextIncludesFindingID` | `server/finding_handoff_test.go` | 触发消息携带 Finding ID |
| `TestVulnDispatcherHandoffAndGoalContract` | `server/finding_handoff_test.go` | 最新提示词契约 |

全量验证：

```bash
go test -p=1 ./...
go vet ./...
git diff --check
```

均通过。

日志：

- `/root/artex-dispatch-assets-go-test.log`
- `/root/artex-dispatch-assets-go-vet.log`
- `/root/artex-dispatch-assets-deploy.log`

## 9. 备份与部署

备份：

- `/root/artex-pre-dispatch-assets-20261029-160528.dump`
- `/root/artex-pre-same-asset-handoff-20261029-161739.dump`

部署后验证：

- `artex-ld-artex-1=healthy`；
- `/api/health` 返回 `ok=true`；
- 正式 Dispatcher prompt version=13；
- 历史任务 #21 回填为 `finding_id=30`、`asset_key=ip:192.168.9.101`；
- 重复任务 #22 未抢占目标记录。

## 10. 边界

- 去重只针对内网渗透派发；漏洞利用、权限提升等场景不共用该唯一约束。
- 资产记录来自 Finding 的 `asset_ids`；完全没有可规范化资产的 Finding 不会被猜测派发。
- 已回填的重复历史任务不会自动删除或改写。
- 未做真实 LLM 派发或真实目标操作；验证由数据库、工具和全量自动化测试完成。
