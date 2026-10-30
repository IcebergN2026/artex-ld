# ARTEX 拓扑证据关系修复实施记录

## 1. 问题

任务 #21 的根节点自动连接其他所有节点，包括没有证据的跨网段主机；任务 #22 又没有入口和边。问题来自后端默认星形连线及入口分类推断，不是前端布局。

## 2. 修复

### db/task_topology.go

- 删除 root → 其他节点的自动 unknown 边。
- 保留入口节点，但入口不产生无证据关系。
- 新增 `RecordTaskTopologyRelation`：校验内网分类、可见节点、自定义关系文本、本任务 finding/fact 证据及无环约束。
- 关系写入 task_topology_overrides，relation_kind=evidence，Manual=false，由 Agent 自动填写证据。

### Agent 工具与提示词

- agent/topology_tools.go 新增每任务 `record_topology_relation`。
- agent/tools.go 的 WorkerTools 加入该工具。
- agent/worker_domain_templates.go 要求先 record_fact，再以 evidence_ref=fact:<id> 登记关系；无证据保持独立节点。
- server/worker_topology_migration.go 只更新内置内网 Worker 当前模板，历史任务快照不改。

### 数据库兼容

task_topology_overrides 的 relation_kind CHECK 增加 evidence；旧表通过 DROP/ADD constraint 迁移。

## 3. 测试

| 测试 | 结果 |
|---|---|
| TestBuildTaskTopologyWithoutAutoEdges | 入口不再自动连线 |
| TestRecordTaskTopologyRelationRequiresEvidenceAndNoCycle | 证据、节点、分类、无环校验 |
| TestWorkerTopologyPromptContract | 工具与提示词存在 |
| TestUpgradeInternalWorkerTopologyPrompt | 当前内网模板幂等迁移 |
| go test -p=1 ./... | 全部通过 |
| go vet ./... | 通过 |
| git diff --check | 通过 |

## 4. 部署与实证

- 备份：`/root/artex-pre-topology-evidence-20261029-163812.dump`。
- Docker 重建成功，artex/postgres healthy。
- `worker_topology_relations_v1=done`。
- 任务 #21：修复前 10 节点/9 无证据边；修复后 10 节点/0 边。
- 任务 #22：8 节点/0 边，不伪造入口。
- 真实 Chrome CDP：10 个真实节点、0 边、无“未建立关系”和 entry:unknown，pageErrors=0。

证据：

`C:\Users\Administrator\Downloads\artex-docs\artex-topology-evidence.png`

`C:\Users\Administrator\Downloads\artex-docs\artex-topology-evidence.json`

## 5. 用户确认

用户后续确认“不需要回滚”，保留本次修复。

未执行真实 Agent 网卡/路由采集或真实目标操作；新关系在 Agent 后续提交证据后生成。
