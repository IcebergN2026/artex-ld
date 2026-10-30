# ARTEX 拓扑移除虚拟“未建立关系”节点实施记录

状态：已实施、已部署、已通过真实 Chrome 验证  
实施日期：2026-10-29

## 1. 问题与根因

用户在内网拓扑中看到额外的“未建立关系”节点。该节点不是资产，也不是 Agent 收集到的真实主机。

根因位于后端 `buildTaskTopology`：

```go
out.RootKey = entry
if out.RootKey == "" && len(out.Nodes) > 0 {
    out.RootKey = "entry:unknown"
}
```

没有入口证据时，后端生成 `entry:unknown`，随后把其他真实节点连接到这个虚拟根。前端再把该 ID 渲染为：

```text
未建立关系
仅表示缺少连接信息
```

问题本质：为了布局而制造的虚拟节点和合成关系被展示成了拓扑内容，容易让用户误认为存在一台额外机器或真实关系。

## 2. 后端修复

文件：`db/task_topology.go`

新规则：

```go
// No entry evidence means no graph root. Keep real nodes independent instead
// of inventing a virtual node or relationships that evidence does not support.
out.RootKey = entry
```

当没有入口证据时：

- `root_key` 保持空字符串。
- 不生成 `entry:unknown`。
- 不生成从虚拟根到真实节点的边。
- 真实节点仍全部返回，由前端独立排列。

存在真实入口时，保留原来的入口节点和未知虚线关系行为。本次只移除无入口情况下的虚拟根，没有删除用户或 Agent 填写的真实关系。

## 3. 前端修复

文件：`web/src/app/(main)/function/tasks/detail/_tabs/pentest-topology-tab.tsx`

删除：

- `entry:unknown` 虚拟节点创建分支。
- “未建立关系”显示文本。
- `virtualStyle`。
- 虚拟根专用的边过滤例外。
- 虚拟根专用的点击排除逻辑。

边现在只渲染两个可见真实节点之间的关系：

```tsx
const edges: Edge[] = data.edges
  .filter((e) => visible.has(e.source) && visible.has(e.target))
```

无根、无边时，现有布局会按行放置真实节点，不需要伪造根节点辅助布局。

## 4. 自动化测试

文件：`db/task_topology_test.go`

测试由：

```text
TestBuildTaskTopologyWithoutEntryUsesExplicitUnknownRoot
```

改为：

```text
TestBuildTaskTopologyWithoutEntryKeepsIndependentRealNodes
```

验证：

- 两个真实 IP 节点仍保留。
- `RootKey == ""`。
- `len(Edges) == 0`。
- 节点中不存在 `entry:unknown`。

原有真实入口、证据引用、自定义关系、备注持久化、环检测和非内网场景 API 测试继续通过。

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

- `/root/artex-topology-no-virtual-go-test.log`
- `/root/artex-topology-no-virtual-go-vet.log`
- `/root/artex-topology-no-virtual-web-build.log`

第一次在重复使用的测试库上遇到既有 `TestCompanyScopeAttribution` 残留数据问题；重建独立测试库后全部通过。未修改该测试或业务断言。

## 5. 部署与真实页面验证

部署：

```bash
docker compose up -d --build artex
```

日志：`/root/artex-topology-no-virtual-deploy.log`  
健康检查：`/api/health` 返回 `ok=true`。

真实任务 #21 的 API 验证：

```text
nodes = 1
edges = 0
root_key = ""
```

真实 Chrome 验证：

- 点击“内网拓扑”页签。
- 页面只显示真实节点 `192.168.9.101 / 内网主机`。
- 节点数量为 1。
- 边数量为 0。
- 不存在“未建立关系”文本。
- 不存在 `entry:unknown`。
- 浏览器页面错误数为 0。

最终结果：

```json
{
  "clicked": true,
  "nodes": ["192.168.9.101\n内网主机"],
  "edges": 0,
  "hasVirtualText": false,
  "hasUnknownID": false,
  "pageErrors": [],
  "pass": true
}
```

证据：

- `C:\Users\Administrator\Downloads\artex-docs\artex-topology-no-virtual.json`
- `C:\Users\Administrator\Downloads\artex-docs\artex-topology-no-virtual.png`

第一次浏览器脚本只调用 DOM `click()`，Radix 页签没有切换；改为真实 CDP 鼠标点击后验证通过。该次失败是测试操作问题，不是拓扑页面异常。

## 6. 数据与兼容边界

- 未新增或修改数据库表。
- 未删除历史资产、关系或备注。
- 只改变无入口证据时的 API 投影和前端展示。
- 真实入口、真实自定义关系和 Agent 证据仍按原逻辑显示。
- 未修改正在运行任务的目标、提示词或状态。
