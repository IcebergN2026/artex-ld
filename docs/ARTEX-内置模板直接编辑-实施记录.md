# ARTEX Planner / Worker 内置模板直接编辑实施记录

## 结论

状态：已实施并部署。

Planner 和 Worker 的内置模板正文现在都能在当前模板的 Sheet 中直接编辑、插入变量、试渲染和保存。内置模板的名称和 `nkey` 继续保留，内置/默认模板仍不可删除。

## 1. 用户可见行为

入口：`Agent → Worker → 模板库`，或 `Agent → Planner → 模板库`。

- 选择“外网渗透”等内置卡片。
- 正文 textarea 不再只读。
- “插入变量”“试渲染”“保存修改”均可用。
- 模板名称仍只读，并显示“内置模板名称保留（作为场景映射键）”。
- “恢复当前场景默认”仍可用；删除仍被保护。

没有恢复为默认的强制动作，用户保存的自定义内置正文会持久化。

## 2. 后端代码

### Worker

`db/worker_templates.go:204`：

```go
var builtin bool
if err := d.QueryRow(`SELECT builtin FROM worker_templates WHERE id=$1`, id).Scan(&builtin); err != nil {
    return nil, err
}
if builtin && name != nil && *name != tpl.Name {
    return nil, ErrWorkerTemplateBuiltin
}
// Body 继续进入通用 PatchWorkerTemplate 更新。
```

### Planner

`db/config.go:1317` 使用同样规则：

```go
if builtin && name != nil && *name != tpl.Name {
    return nil, ErrPlannerTemplateBuiltin
}
```

`server/worker_templates.go:87` 和 `server/planner_templates.go:96` 的 409 文案均改为：

```text
内置模板名称必须保留；正文可直接编辑
```

身份保留让 `WorkerDefaultTemplateBodyFn` / `PlannerDefaultTemplateBodyFn` 仍能按内置 `nkey` 找到模板。正文变更不会改写既有任务的 `worker_prompt_override` / `planner_prompt_override` 快照。

## 3. 前端代码

- `web/src/components/worker-template-manager.tsx`：正文不再有 `builtin ? "（只读）" : ""`；移除 `body.readOnly`；保存按钮不再因内置禁用。
- `web/src/components/planner-template-manager.tsx`：同样的正文、变量、试渲染和保存行为。
- 两个组件的 `nameReadOnly={builtin}`、内置/默认删除保护保持不变。

## 4. 自动化测试

| 测试 | 覆盖 |
|---|---|
| `TestBuiltinWorkerTemplateBodyEditable` | Worker 正文可改；名称/builtin/default不变；改名和删除拒绝 |
| `TestBuiltinPlannerTemplateBodyEditable` | Planner 同样保护 |
| `go test -p=1 ./...` | 全量通过 |
| `go vet ./...`、`npx tsc --noEmit`、`git diff --check` | 通过 |

日志：`/root/artex-worker-builtin-edit-*.log`、`/root/artex-planner-builtin-edit-*.log`。

## 5. 真实 Chrome 验证

Worker 和 Planner 都分别执行：

1. 打开真实模板库并选择内置外网渗透。
2. 确认正文可编辑、名称只读。
3. 用 CDP `Input.insertText` 输入临时 marker。
4. 点击“保存修改”。
5. API 验证 marker 已保存，builtin/default身份不变。
6. 刷新页面，验证正文仍可编辑且 marker 持久化。
7. 通过 API 恢复保存前正文，确认恢复成功。

| 场景 | 证据 |
|---|---|
| Worker | `artex-worker-builtin-editable.png`、`artex-worker-builtin-edit-result.json` |
| Planner | `artex-planner-builtin-editable.png`、`artex-planner-builtin-edit-result.json` |

证据位于 Windows `C:\Users\Administrator\Downloads\artex-docs\`。截图保留的是测试过程状态，最终正文已恢复，临时 marker 不留在正式库。

## 6. 部署与边界

- Docker 健康检查通过。
- 无数据库结构变更。
- 没有修改 Worker/Planner 的内置默认提示词语义；本功能只开放编辑能力。
- 修改内置正文只影响之后解析到的默认模板，不追溯修改历史任务快照。
