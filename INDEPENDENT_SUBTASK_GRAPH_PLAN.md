# 子任务使用独立探索链路，仅复制 Finding 血缘链

## Context

用户要求编排子任务使用新的“探索链路”，不再继承父任务的全部内容；随后明确希望只获取触发 Finding 的完整 `intent-fact/finding` 链，其他内容不获取，并选择“复制为独立节点”而非只读引用。现状中 `db.CreateTaskWithOptions` 已新建独立 `explorations` 和 origin fact；父图来自 `spawn_task` 将 `source_task_ids` 写入 `task_relations` 后由 `DirectSourceStores` 动态合并。

目标行为：`spawn_task` 不再创建来源关系。带 `finding_id` 创建子任务时，仅把该 Finding 沿 `yields/derived_from` 反向可达的 `intent/fact/finding` 祖先子图复制为子任务自己的新节点和新边；共享祖先若确实在血缘路径上保留，无关分支、goal、digest、活动及其他来源上下文不复制。`parent_ref`、审核与 LLM 配置继承、派发资产去重保持原行为。

## Approach

### 1. 取消 `spawn_task` 的全量来源继承

编辑 `server/orchestration.go` 的 `toolSpawnTask`：

- 从 JSON Schema 删除 `source_task_ids`，从匿名输入结构删除 `SourceTaskIDs []string`，删除 `normalizeSourceTaskIDs(a.SourceTaskIDs, ...)` 校验块、局部 `sourceIDs` 和 `TaskCreateOptions.SourceTaskIDs` 赋值。
- 将工具描述改为：`新建一个子任务并启动探索引擎，返回 task_id。子任务使用独立探索链路；带 finding_id 时仅复制该 Finding 的完整祖先链。parent_ref 可选，仅用于父子关联。`
- 将 `finding_id` 参数说明补充为：同时用于派发资产去重和选择要复制的 Finding 血缘链。
- `parent_ref` 继续调用 `SetParentRef`；`resolveSpawnReview`、父任务 LLM pin、`findingDispatchAssetKeys`、`DispatchFindingID`、`launchTask` 不变。旧调用即使仍传 `source_task_ids`，现有 `json.Unmarshal` 也只会将其作为未知字段忽略，不会再生成 `task_relations`。
- 在 `db.TaskCreateOptions` 传入 `CopyFindingChain: a.FindingID > 0`。HTTP 新建任务的人工 `source_task_ids`、通用 `task_relations`、归档恢复和前端继承展示保持原样；不批量删除已有关系。

### 2. 在任务创建事务内复制所选 Finding 血缘链

在 `db/tasks.go` 的 `TaskCreateOptions` 新增 `CopyFindingChain bool`。它只由 `spawn_task` 设置，避免改变其他使用 `DispatchFindingID` 做资产去重的 DB 调用方。

在 `CreateTaskWithOptions` 中，任务行和 `insertTaskDispatchTargets` 成功后、`insertTaskRelations` 前执行：

```go
if opts.CopyFindingChain {
    if opts.DispatchFindingID <= 0 {
        return nil, fmt.Errorf("finding_id is required to copy finding chain")
    }
    if err := copyFindingChainTx(tx, expID, opts.DispatchFindingID); err != nil {
        return nil, err
    }
}
```

任何来源缺失或复制错误都回滚新 exploration、任务、派发目标和链，不降级为空图或全量继承。

新建 `db/finding_chain_copy.go`，实现：

```go
func copyFindingChainTx(tx *sql.Tx, dstExplorationID, findingID int64) error
```

该函数是唯一的跨 exploration 链复制实现，因为现有 `AddNode`、`Link`、`Anchor` 都是独立事务，无法保证子任务与链原子提交。具体行为：

1. 通过 `findings f JOIN tasks t ON f.task_id=t.id` 按 `findingID` 读取 `f.node_id`、来源 `t.exploration_id`；要求来源任务未删除、`node_id` 非空且对应节点是该 exploration 中的 `kind='finding'`。缺失、跨图不一致或类型错误均返回错误。
2. 复用 `FindingLineage` 的递归 CTE 读法，但把共享读取实现抽成私有 helper，使 `*db.DB` 与 `*sql.Tx` 均可查询。公开 `FindingLineage` 保持原有“所有边类型”的行为；复制专用调用在递归和最终边查询中都限制 `e.rel IN ('yields','derived_from')`，并在递归经过的源节点及最终节点查询中限制 `n.kind IN ('intent','fact','finding')`。这样不会穿过 goal、hint、digest 或 covers/proves 边拉入其他内容。
3. 子任务 origin fact 已在 `CreateTaskWithOptions` 创建。来源 `state='origin'` 的 fact 不重复插入，而是映射到子任务 origin；其余节点按来源 id 升序插入并建立 `oldID -> newID` 映射：
   - 保留 `kind/payload/priority/origin/owner/created_at`；`content_version` 使用新节点默认值，冷 digest 元数据不复制。
   - 复制出的 intent 一律落 `state='done'`，作为历史链快照，不进入子任务 frontier，不保留 `blocked_reason/delete_reason`。
   - fact/finding 保留原 `state` 和非空原因字段。
   - finding 节点的 JSONB payload 合并 `source_finding_id=<原 findings.id>` 和 `source_finding_node_id=<原 exploration_nodes.id>`，沿用 `AddFindingFollowUpIntent` 已有的来源键命名。**不新增或改指向任何 `findings` 表行**，避免全局 Finding 重复及破坏父任务 lineage；原行仍属于父任务。
4. 仅为复制节点复制其 `exploration_anchors`，把 `node_id` 换成新 id、资产 id 保持不变。通过 `INSERT ... SELECT ... ON CONFLICT DO NOTHING` 复用现有锚点写入模式，不带入父任务其他资产范围。
5. 按映射复制链内 `yields/derived_from` 边；来源 origin 端点替换为子任务 origin，保留 `created_at`。若两端映射后相同则跳过自环。先插节点和锚点、再插边，满足外键。
6. 不复制 `activity`、`task_constraints`、流量绑定、会话、goal、hint、digest、其他兄弟分支，也不建立 `task_relations`。

### 3. 让 Dispatcher 提示词与选择性复制契约一致

编辑 `agent/promptcatalog.go` 的 `VulnDispatcherDefaultPrompt`，将当前行：

`- parent_ref = 触发任务 id；source_task_ids = [触发任务 id]（只读继承父任务已探明资产/结论/证据，不复制父 task_constraints，也不把立足点当成唯一终点）。`

替换为：

`- parent_ref = 触发任务 id，只记录父子关系；子任务使用独立探索链路，仅复制触发 Finding 的完整 intent-fact/finding 祖先链，不传递 source_task_ids，也不复制父 task_constraints。父任务其他节点、活动和来源上下文不导入；触发 Finding、立足点、凭据和起点证据必须写入 description，不能把父任务其他结果当作子任务已探明结果。`

在 `config-export/agent_prompts_current.jsonl` 的 `vuln_dispatcher` 行执行同一替换，保留文件现有转义约定。七段 `description` 规则、资产去重及“不复制父任务的单资产操作约束”不变。

### 4. 刷新已有数据库的 Schema 和默认提示词

- 在 `server/orchestration.go` 的 `refreshBuiltinToolSchemas` 中，将标记改为 `tool_schema_refresh_v14_independent_spawn`，启动时把 `spawn_task` 的新描述和不含 `source_task_ids` 的 Schema 刷入已有数据库。
- 在 `server/finding_orchestration_migration.go` 新增 `func (s *Server) upgradeVulnDispatcherIndependentGraph()`，并在 `seedOrchestrationTools` 中紧接 `upgradeVulnDispatcherFindingPrompt()` 后调用。
- 迁移设置键为 `vuln_dispatcher_independent_graph_v1`。默认血统必须同时包含 `漏洞派发 agent`、`审核开关由人决定`、`去重的键是`；仅当默认血统且含旧标记 `source_task_ids = [触发任务 id]` 时，调用 `ResetPromptToDefault(a.ID, s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt))` 追加并切换新默认版本。
- 无旧标记时不重写；自定义血统即使含旧字段也不覆盖，代码仍保证不建立来源关系。读取或 `ResetPromptToDefault` 失败时不写完成标记，下次启动重试；成功或无需迁移后才将标记设为 `true`。

### 5. 增加真实图复制与迁移回归测试

在 `server/vuln_dispatcher_nkey_test.go` 新增 `TestSpawnTaskCopiesOnlyFindingChain`：

- 复用 `fiFixture` 的取消上下文，避免真实 LLM 工作。确保默认 Worker 模板和“内网渗透”分类存在，分别复用 `SeedWorkerTemplateIfEmpty`、`SeedTaskCategoryPresetIfEmpty`。
- 父任务构造 `origin → intent → fact → finding` 链，并添加一个无关 intent/fact 分支及不属于链的锚点。Finding 使用真实资产和 `findings` 行，复制源 intent 可保持 running，以验证导入后不会成为子任务活动 frontier。
- 调 `spawn_task` 时传 `finding_id`、对应 `target_asset`、父 `parent_ref`、`planner_template_nkey=内网渗透`、非空 `planner_prompt`，并故意再传旧 `source_task_ids=[父id]`。
- 断言创建成功且 `ParentRef` 保留、`SourceTaskIDs` 为空、`task_relations` 为 0。
- 调 `explorationGraph` 读取子任务：只含新 origin 和复制的 intent/fact/finding 及重映射边；包含链摘要，不包含无关分支标记，不包含 `"inherited":true`。所有复制节点 id 均不同于来源 id，复制 intent 为 `done`，finding payload 含正确 `source_finding_id/source_finding_node_id`。
- 断言只复制链节点锚点，父 `findings` 行数保持 1 且仍指向原任务/节点，没有重复 Finding。

在 `server/finding_migrations_test.go` 新增 `TestUpgradeVulnDispatcherIndependentGraph`：

- 复用 `capturePromptState`。默认血统 fixture 同时含三个血统标记和旧 `source_task_ids` 指令；迁移后切到不含旧指令的新默认版本，标记为 `true`，再次调用不新增版本。
- 自定义 fixture 含旧字段但缺少默认血统标记；迁移后正文和当前版本不变。

## Critical files & anchors

- `server/orchestration.go` — `toolSpawnTask` 的 Schema、`CopyFindingChain` 输入、任务创建调用及 Schema 刷新标记。
- `db/tasks.go` — `TaskCreateOptions` 和 `CreateTaskWithOptions` 的原子事务，复制必须位于 commit 前。
- `db/finding_chain_copy.go` — 新链复制 helper；来源校验、origin/节点/边/锚点重映射和错误回滚的核心。
- `server/finding_orchestration_migration.go` — 默认血统识别、自定义保护及一次性提示词迁移。
- `server/vuln_dispatcher_nkey_test.go` — 新 `spawn_task` 选择性链复制的端到端回归测试。

## Verification

1. 在仓库根目录运行：
   ```bash
   go test ./agent
   ARTEX_SKIP_FRONTEND=1 bash build.sh --target linux/amd64
   ```
   前端未改，复用 `server/webui/dist`。

2. 用隔离 PostgreSQL 数据库执行新增行为测试，不使用生产 `artex` 库：
   ```bash
   tar -czf dist/independent-graph-test-src.tar.gz agent cmd config db enrich evidence guard intercept llmpool llmrec mcphttp notify report selfupdate server sidequestion third_party traffic xlog go.mod go.sum
   scp dist/independent-graph-test-src.tar.gz linux-artex:/tmp/
   ssh linux-artex 'set -eu; SRC=$(mktemp -d /tmp/artex-independent-graph-test.XXXXXX); DB=artex_independent_graph_$(date +%s); cleanup(){ runuser -u postgres -- dropdb --if-exists --force "$DB" >/dev/null 2>&1 || true; rm -rf "$SRC" /tmp/independent-graph-test-src.tar.gz; }; trap cleanup EXIT; tar -xzf /tmp/independent-graph-test-src.tar.gz -C "$SRC"; runuser -u postgres -- createdb -O artex "$DB"; PASS=$(python3 -c '"'"'import json; print(json.load(open("/opt/artex/config.json"))["database"]["password"])'"'"'); cd "$SRC"; ARTEX_PG_DSN="postgres://artex:${PASS}@127.0.0.1:5432/${DB}?sslmode=disable" go test ./server -run "TestSpawnTaskCopiesOnlyFindingChain|TestUpgradeVulnDispatcherIndependentGraph" -count=1'
   ```
   两个测试必须实际运行并通过；跳过视为失败，不改用生产库。临时库、目录和上传包由 trap 清理。

3. 测试成功后上传 `dist/artex-dev-linux-amd64.deploy.tar.gz` 及 `.sha256` 到 `linux-artex:/root/artex-redeploy/`，远程 `sha256sum -c`，解包并执行 `bash ./install.sh --server /root/artex-redeploy/artex-dev-linux-amd64.deploy.tar.gz`。通过现有 `/bin/sh ./start.sh` 守护进程优雅停止 ARTEX，再从 `/opt/artex` 执行 `setsid -f nohup ./start.sh >> artex.log 2>&1 < /dev/null`。
   ```bash
   curl --retry 20 --retry-connrefused --retry-delay 1 -fsS http://127.0.0.1:8787/api/health
   ```
   预期返回 `{"ok":true,"service":"artex","version":"dev"}`。

4. 在远程 `artex` 库只读核验：
   - 当前默认 `vuln_dispatcher` 提示词不含 `source_task_ids = [触发任务 id]`，包含“仅复制触发 Finding 的完整 intent-fact/finding 祖先链”。
   - `tools` 表 `spawn_task` 的 `schema->'properties'` 不含 `source_task_ids`。
   - `tool_schema_refresh_v14_independent_spawn` 和 `vuln_dispatcher_independent_graph_v1` 均为 `true`。
   若远程 Dispatcher 被识别为自定义提示词，不覆盖用户文本；记录该状态，Schema 与链复制行为测试仍必须通过。

## Assumptions & contingencies

- “子任务”指 `spawn_task` 创建的编排子任务，包括 `vuln_dispatcher` 派生任务；HTTP 新建任务的人工来源关联能力保留。
- “完整链条”采用完整祖先 DAG，而非只取直接 intent 或人为选择一条最短路径；仅沿 `yields/derived_from` 反向遍历，真正位于路径上的共享祖先保留，其他分支不复制。
- 复制后节点属于子任务，父任务后续变化不同步；导入 intent 是 `done` 状态的历史快照。父 `findings` 行及证据流量不复制，复制 finding 节点仅以来源键引用原记录。
- 仅新建子任务应用该行为，不批量修改已有关系。若部署时远程出现已有子任务，保留其关系并报告，不猜测是否为人工关联。
