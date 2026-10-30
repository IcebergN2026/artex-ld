# ARTEX 派发范围与内网拓扑场景实施记录

状态：已实施、已部署  
实施日期：2026-10-29  
对应需求：子任务继承证据但不默认继承单资产操作约束；内网拓扑仅用于内网渗透场景。

## 1. 最终实现

### 1.1 Dispatcher 范围语义

保留：

- `source_task_ids` 对父任务资产、发现和证据的只读继承。
- 用户明确给出的授权边界和禁止项。
- 运行中同资产内网子任务的 Finding 追加机制。

取消：

- 默认把立足点主机写成唯一测试目标。
- 复制父任务单次操作中的单资产限制。
- 把父任务 `task_constraints` 当成子任务必须逐项复制的模板。

子任务范围根据其目标重新生成，可覆盖父任务证据中的多个已知内网资产。可达性仍不等于授权。

### 1.2 内网拓扑场景判定

统一使用：

```text
task_categories.nkey = 内网渗透
```

不按任务名称、父任务、私网 IP、固定分类 ID 或 goal 文本判断。

实现过程中发现分类重命名原本会同时修改 `nkey`。现已对内网场景保留 `nkey`，仅允许显示名称变化，避免重命名后拓扑能力消失。

## 2. Dispatcher 提示词变更

文件：`agent/promptcatalog.go`  
配置：`config-export/agent_prompts_current.jsonl`、`artifacts/config-export/agent_prompts_current.jsonl`  
正式库：Dispatcher 当前提示词版本 10。

### 2.1 证据继承，不复制执行约束

```text
parent_ref = 触发任务 id；source_task_ids = [触发任务 id]
（只读继承父任务已探明资产/结论/证据，不复制父 task_constraints，
也不把立足点当成唯一终点）。
```

### 2.2 子任务独立生成目标范围

```text
目标范围：根据子任务目标重新生成，可覆盖父任务证据中多个已知内网资产，
不继承父任务“只测试某一资产”的单次操作约束，不默认生成单资产限制。
用户明确给出的授权边界和禁止项仍是上限；不得把“直连可达”写成已授权。
未明确提供范围时不臆造目标，也不自行新增限制。
```

### 2.3 安全边界

```text
只保留用户明确给出的停止服务、删改数据、DoS、持久化等安全与授权边界，
不复制父任务的单资产操作约束，不自行放松明确禁令。
```

`goal` 相应改为引用 `description` 的“明确目标范围”，不再要求子任务重复父任务的单资产约束。

## 3. `category_nkey` 全链路

### 3.1 数据库模型和查询

文件：`db/tasks.go`

- `Task` 新增 `CategoryNKey string`。
- `taskCols` 增加分类 `nkey` 查询。
- `scanTask` 增加对应扫描字段。
- 新建任务返回的内存 DTO 同时填充 `CategoryName` 和 `CategoryNKey`，避免新建后首次读取为空。

核心查询：

```sql
SELECT category.name, category.nkey
FROM task_categories category
WHERE category.id = tasks.category_id
```

### 3.2 内网场景键稳定

文件：`db/task_categories.go`，`RenameTaskCategory`

```go
if current.NKey == "内网渗透" {
    nkey = current.NKey
}
```

显示名称可以修改，内网场景 `nkey` 不随重命名变化。未新增数据库列或表。

### 3.3 服务端生命周期与 DTO

文件：

- `server/manager.go`
- `server/dto.go`

处理内容：

- `server.Task` 增加 `CategoryNKey`。
- `taskFromPG` 透传数据库字段。
- `taskLifecycleState` 保存场景键。
- 单个/批量修改分类、删除分类和重命名分类时同步更新场景键。
- `TaskDTO` 输出 `category_nkey`。

这一层是浏览器验证发现“内网任务仍不显示拓扑”后补齐的；只改数据库结构体不足以让前端收到字段。

## 4. 后端拓扑访问控制

文件：`server/task_topology.go`

三个接口共用 `topologyTask`：

```go
t, ok := s.m.Task(r.PathValue("id"))
if !ok || t.CategoryNKey != "内网渗透" {
    writeErr(w, http.StatusNotFound, "内网拓扑仅适用于内网渗透任务")
    return nil, false
}
```

受影响接口：

```text
GET   /api/tasks/{id}/pentest-topology
PATCH /api/tasks/{id}/pentest-topology/nodes/{ip}
POST  /api/tasks/{id}/pentest-topology/nodes/{ip}/notes
```

非内网任务返回：

```http
HTTP 404
```

```json
{"error":"内网拓扑仅适用于内网渗透任务"}
```

历史拓扑表和已有数据未删除。

## 5. 前端控制

文件：`web/src/app/(main)/function/tasks/detail/page.tsx`  
类型：`web/src/lib/types.ts`

```tsx
const topologyEnabled = task.category_nkey === "内网渗透";
const visibleTabs = TABS.filter(
  (item) => item.value !== "topology" || topologyEnabled
);
```

- 页签列表使用 `visibleTabs`。
- 仅 `topologyEnabled` 时挂载 `PentestTopologyTab`。
- 外网任务不会发起拓扑 API 请求。
- 外网父任务派生的内网子任务正常显示拓扑。

## 6. 自动化测试

### 6.1 场景键稳定

`db/task_category_nkey_test.go`

```text
TestTaskCategoryNKeyStableAcrossRename
```

验证：显示名称改变后，任务仍保留 `category_nkey=内网渗透`。

### 6.2 三个 API 和父子场景

`server/task_topology_scope_test.go`

```text
TestTopologyRoutesRequireInternalCategoryNKey
```

子测试：

```text
read
relation
note
```

覆盖：

- 外网任务三个 API 均返回 404。
- 外网父任务的内网子任务返回 200。
- 分类显示名改变后内网子任务仍返回 200。
- `TaskDTO` 不丢失 `category_nkey`。
- 运行中重新分配分类时同步更新场景键。

### 6.3 Dispatcher 契约

相关测试：

```text
server/vuln_dispatcher_nkey_test.go::TestVulnDispatcherDefaultPromptUsesStableTemplateKeys
server/vuln_dispatcher_foothold_test.go::TestVulnDispatcherRequiresEstablishedFoothold
agent/execution_mode_test.go::TestVulnDispatcherDualModeAndGoalContract
```

验证新范围语义存在，原立足点门槛、双模式和配置导出一致性不回退。

### 6.4 测试环境修正

新增测试最初使用 `defer Close`，导致数据库在 `t.Cleanup` 前关闭，分类和任务残留影响后续 Review 测试。已改为最后注册关闭动作，让数据清理先执行。

最终验证：

```bash
ARTEX_PG_DSN=... go test -p=1 ./...
go vet ./...
cd web && npx tsc --noEmit
cd web && npm run build:static
git diff --check
```

全部通过。

日志：

- `/root/artex-topology-scope-go-test.log`
- `/root/artex-topology-scope-go-vet.log`
- `/root/artex-topology-scope-web-build.log`

## 7. 真实 Chrome 验证

使用现有真实数据，不修改拓扑：

| 对象 | 场景 | 结果 |
| --- | --- | --- |
| 任务 #17 | 外网渗透 | 无内网拓扑页签 |
| 任务 #17 | 外网渗透 | 页面拓扑请求数为 0 |
| 任务 #17 三个拓扑 API | 外网渗透 | 全部 404 |
| 任务 #18，父任务 #17 | 内网渗透 | 显示内网拓扑页签 |
| 任务 #18 | 内网渗透 | 页面发起一次拓扑请求，API 200 |

浏览器最终结果：

```json
{
  "externalTabAbsent": true,
  "externalTopologyRequests": 0,
  "internalTabPresent": true,
  "internalTopologyRequests": 1,
  "pass": true
}
```

证据：

- `C:\Users\Administrator\Downloads\artex-docs\artex-topology-scope-browser.json`
- `C:\Users\Administrator\Downloads\artex-docs\artex-external-no-topology.png`
- `C:\Users\Administrator\Downloads\artex-docs\artex-internal-child-topology.png`

首次浏览器脚本使用错误详情路径，随后修正；第一次通过条件还要求 API 返回 `parent_ref`，但 `TaskDTO` 本就不输出该字段，父子关系改由数据库核验。两次都是测试脚本问题，未为此扩大生产 API。

## 8. 部署和备份

数据库备份：

```text
/root/artex-pre-topology-scope-20261029-140953.dump
```

部署：

```bash
docker compose up -d --build artex
```

日志：

```text
/root/artex-topology-scope-deploy.log
```

结果：

- Dispatcher 当前提示词版本 10。
- 正式库包含“不复制父 task_constraints”和“不默认生成单资产限制”。
- `artex-ld-artex-1` 为 `healthy`。
- `/api/health` 返回 `ok=true`。
- 正式库确认任务 #18 的父任务为 #17，分类为内网渗透。

## 9. 边界

- 未执行真实 LLM 自主派发或目标操作。
- 未批量修改历史任务目标、约束或提示词快照。
- 未删除外网历史拓扑数据。
- “不继承单资产操作约束”不等于忽略用户明确授权边界。
- 本需求只限制拓扑显示和访问，没有实施此前讨论过的拓扑布局或证据模型重构。
