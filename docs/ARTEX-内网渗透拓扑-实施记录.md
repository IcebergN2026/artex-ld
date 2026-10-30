# ARTEX 内网渗透拓扑实施记录

> 状态：代码、全量测试、真实 Chrome 验证和 Docker 部署已完成；尚未提交 Git。
> 入口：任务详情 `/function/tasks/detail?id=<id>` 的“内网拓扑”Tab。
> 本文以最新“自定义关系、节点备注、Agent 自动证据、无入口状态编辑”需求为准。

## 1. 最终实现

- 一个内网 IP 显示为一个节点。
- 内网节点显示：主机名/IP、节点显示名称、最新人工备注摘要。
- 备注保存后，节点立即使用 API 返回的最新图重新渲染；刷新后仍保留。
- 连接关系是用户或 Agent 填写的自由文本，不再让用户选择“横向移动/凭据使用/关系待确认”。
- 人工编辑区没有证据引用输入；已有证据由 Agent/后端自动维护，人工保存不会清空它。
- 页面不显示或编辑入口状态。历史 API 字段暂时保留，避免破坏已有数据和客户端。
- 没有连接信息的自动 `unknown` 边仍使用虚线；人工自定义关系使用实线并显示原文。
- 拒绝自环、关系环和跨任务父节点；原始资产和发现不被人工编辑覆盖。

## 2. 文件与职责

| 文件 | 当前实现 |
|---|---|
| `db/schema.sql` | `task_topology_overrides`、`task_topology_notes`；新增 `relation_label` |
| `db/task_topology.go` | 节点/边投影、自由关系、备注、环与任务边界校验 |
| `db/task_topology_test.go` | 关系、证据兼容、备注和拓扑生成测试 |
| `server/task_topology.go` | 拓扑查询、节点 PATCH、备注 POST |
| `server/server.go` | 三条拓扑路由 |
| `web/src/app/(main)/function/tasks/detail/_tabs/pentest-topology-tab.tsx` | React Flow、自由关系输入、节点备注渲染和抽屉 |
| `web/src/app/(main)/function/tasks/detail/page.tsx` | 任务详情“内网拓扑”Tab |
| `web/src/lib/types.ts` | `TaskTopologyEdge.label` 和新的节点更新 DTO |
| `web/src/lib/api.ts` | 三个拓扑 API 客户端 |
| `server/core_test.go` | 仅测试隔离：清除其他测试留下的激活 LLM，避免目标种子依赖外部请求 |

没有新增 npm 依赖，继续复用 `@xyflow/react`。

## 3. 数据模型

```sql
CREATE TABLE IF NOT EXISTS task_topology_overrides (
    task_id       BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_ip       TEXT NOT NULL,
    relation_kind TEXT NOT NULL DEFAULT '',
    relation_label TEXT NOT NULL DEFAULT '',
    parent_ip     TEXT NOT NULL DEFAULT '',
    evidence_ref  TEXT NOT NULL DEFAULT '',
    entry_status  TEXT NOT NULL DEFAULT '',
    display_label TEXT NOT NULL DEFAULT '',
    updated_by    TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, node_ip)
);

CREATE TABLE IF NOT EXISTS task_topology_notes (
    id         BIGSERIAL PRIMARY KEY,
    task_id    BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_ip    TEXT NOT NULL,
    body       TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

已有数据库通过幂等迁移补充：

```sql
ALTER TABLE task_topology_overrides
  ADD COLUMN IF NOT EXISTS relation_label TEXT NOT NULL DEFAULT '';
ALTER TABLE task_topology_overrides
  ADD COLUMN IF NOT EXISTS display_label TEXT NOT NULL DEFAULT '';
```

`relation_kind` 和 `entry_status` 没删：它们用于兼容历史数据。新人工关系内部保存为 `kind=unknown, manual=true, relation_label=<用户原文>`；前端不再把内部 kind 翻译给用户选择。

## 4. 后端自由关系逻辑

`TaskTopologyEdge` 新增：

```go
Label string `json:"label,omitempty"`
```

`TaskTopologyOverride` 新增：

```go
RelationLabel string `json:"relation_label"`
```

人工 PATCH 使用：

```go
label := strings.TrimSpace(in.RelationLabel)
if len(label) > 200 { return fmt.Errorf("relation label too long") }
if in.ParentIP != "" && label == "" { return fmt.Errorf("relation label required") }
if label != "" && in.ParentIP == "" { return fmt.Errorf("parent node required") }
```

人工保存时：

```go
if label != "" {
    in.EvidenceRef = existing.EvidenceRef // 保留 Agent 自动证据
}
in.RelationLabel = label
// manual=true；kind 仅作内部兼容，不再作为用户选项。
```

边投影：

```go
if edge.Manual && edge.ParentIP != "" {
    out.Edges = append(out.Edges, TaskTopologyEdge{
        Source: edge.ParentIP, Target: n.IP,
        Kind: edge.RelationKind, Label: edge.RelationLabel,
        EvidenceRef: edge.EvidenceRef, Manual: true,
    })
}
```

`addTopologyEdge` 对自动边和人工边保留 Agent 提供的证据；人工自由文本本身不再要求用户选择证据。

## 5. API

```text
GET   /api/tasks/{id}/pentest-topology
PATCH /api/tasks/{id}/pentest-topology/nodes/{ip}
POST  /api/tasks/{id}/pentest-topology/nodes/{ip}/notes
```

最新 PATCH 请求：

```json
{
  "relation_label": "通过共享运维终端访问",
  "parent_ip": "10.99.0.1",
  "display_label": "核心数据库"
}
```

不再接收人工 `relation_kind`、`evidence_ref` 或 `entry_status`。服务端保留已有证据和入口字段，但页面不使用它们。

备注请求：

```json
{"body":"核心数据库，仅允许只读验证"}
```

## 6. 前端节点与备注

内网节点使用最新一条备注：

```tsx
const latestNote = n.internal ? data.notes[n.ip]?.[0]?.body : "";
const label = `${title}\n${displayLabel}` +
  (latestNote ? `\n备注：${notePreview(latestNote)}` : "");
```

备注按空白压缩并截断为 80 个 Unicode 字符，完整正文仍在抽屉的备注历史中显示。

保存备注继续使用同一个服务端响应：

```tsx
setData(await api.addTaskTopologyNote(taskId, selected.ip, note));
```

由于 `layoutTopology` 现在消费 `data.notes`，这次 `setData` 会立即更新节点，而不是只更新抽屉历史。

边显示：

```tsx
label: e.manual ? e.label || "自定义关系" : "",
strokeDasharray: e.manual ? undefined : "6 5",
```

固定 `EDGE_LABEL`、三类关系 Badge、证据输入和入口状态控件均已删除。

## 7. 测试到代码映射

| 测试 | 对应实现和断言 |
|---|---|
| `TestTaskTopologyManualRelationAndNotePersistence` | `db/task_topology.go:UpdateTaskTopologyNode`、`AddTaskTopologyNote`；自由文本去空格、无人工证据也可保存、保留返回 notes |
| 同一测试的长度分支 | 关系文本最多 200 字节；显示名称最多 120 字节 |
| 同一测试的父节点分支 | 有文本无父节点必须失败；自环/关系环必须失败 |
| `TestBuildTaskTopologyUnknownEdges` | `buildTaskTopology`；自动未知边保持虚线语义 |
| `TestBuildTaskTopologyEntry` | 历史入口投影仍兼容，前端不展示入口编辑 |
| `TestCoreTaskLifecyclePG` | `server/core_test.go` 测试开始时清除激活/轮询 LLM；只验证目标种子，不依赖外部 LLM |
| 全量 `go test -p=1 ./...` | 全部包通过，日志 `/root/artex-topology-custom-go-test.log` |
| `go vet ./...` | 通过，日志 `/root/artex-topology-custom-go-vet.log` |
| `npx tsc --noEmit` | 通过 |
| `npm run build:static` | 通过，日志 `/root/artex-topology-custom-web-build.log` |
| `git diff --check` | 通过 |

## 8. 真实 Chrome 验证

使用临时任务 `#12` 和文档 IP `10.99.0.1/10.99.0.2`，没有连接任何真实目标。

验证结果：

1. 节点抽屉存在 `#topology-relation-label` 自由文本框。
2. 页面没有“横向移动/凭据使用/关系待确认”选项或 Badge。
3. 页面没有证据引用输入和入口状态。
4. 通过真实 CDP 输入“通过共享运维终端访问”，选择上游并保存；边显示原文。
5. 输入备注“核心数据库，仅允许只读验证”；`10.99.0.2` 节点立即显示该备注。
6. 刷新页面后，关系和节点备注均持久化。

证据：

- Windows：`C:\Users\Administrator\Downloads\artex-docs\artex-topology-custom-relation-note.png`
- Windows：`C:\Users\Administrator\Downloads\artex-docs\artex-topology-custom-browser-result.json`

临时任务、资产、备注、短期 JWT 和独立测试数据库均已删除。Docker 最终 `/api/health` 为 `ok=true`。

## 9. 部署与回滚

- 部署前 PostgreSQL 备份路径记录在 `/root/artex-topology-custom-backup-path`，文件权限 0600。
- Docker 日志：`/root/artex-topology-custom-deploy.log`。
- 回滚代码不会自动删除 `relation_label`；这是向后兼容的新增列。
- 只有明确需要清除人工拓扑数据时，才单独备份并处理两张拓扑表；不要把表删除混入普通应用回滚。
