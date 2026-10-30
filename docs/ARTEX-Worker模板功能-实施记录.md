# ARTEX Worker 模板功能实施记录

- 状态：已实施、已测试、已部署。
- 部署地址：`http://192.168.9.104:8787/`。
- 实施范围：Worker 模板库、场景默认 Worker 模板、新建任务 Worker 模板与提示词覆盖、任务正文快照、子任务场景映射、配置导入导出。
- 明确不包含：修改 Planner 模板结构；给 Dispatcher 增加新的 Worker 参数；修改 Windows ShardX MCP。

## 1. 需求与实际行为

### 1.1 实施前

Worker 只有以下提示词来源：

```text
数据库 agents/worker 全局覆盖 → agent/worker.go 内置 workerDefaultTmpl
```

任务和场景不能选择 Worker 模板。Planner 已有模板库和任务快照，Worker 没有对应能力。

### 1.2 实施后

用户可以在两个位置配置：

1. `任务 → 场景管理 → 场景预设 → 默认 Worker 模板`。
2. `任务 → 新建任务 → 高级设置 → Worker 模板 / Worker 提示词覆盖`。

新建任务旁的 `管理` 可以创建、编辑、删除、设默认和试渲染 Worker 模板。

任务创建后保存完整正文到 `tasks.worker_prompt_override`。之后修改或删除模板，不改变已创建任务。

## 2. 提示词规则

### 2.1 允许的模板变量

只允许 `agent/prompt.go` 的 `WorkerVars`：

```go
type WorkerVars struct {
    ProxyAddr  string
    WorkerName string
    DataDir    string
    Now        string
}
```

模板示例：

```gotemplate
当前 Worker：{{.WorkerName}}
代理地址：{{.ProxyAddr}}
数据目录：{{.DataDir}}
当前时间：{{.Now}}
```

`{{.Goal}}`、`{{.Plan}}` 等 Planner 变量会被拒绝，不会静默变成空字符串。

### 2.2 运行时优先级

`agent/worker.go:305` 的 `workerSystem` 使用以下顺序：

```text
1. tasks.worker_prompt_override 非空：使用任务快照。
2. 数据库 agent_prompts_current 中 worker 全局覆盖非空：使用该覆盖。
3. worker_templates 默认模板正文非空：使用默认模板。
4. agent/worker.go 内置 workerDefaultTmpl。
```

核心代码：

```go
base := strings.TrimSpace(taskOverride)
if base == "" {
    base = strings.TrimSpace(globalPromptOverride("worker"))
}
if base == "" {
    base = strings.TrimSpace(workerDefaultTemplateBody())
}
if base == "" {
    base = workerDefaultTmpl
}
rendered := renderSystem("worker", base, WorkerVars{
    ProxyAddr: proxyAddr, WorkerName: workerName,
    DataDir: dataDir, Now: time.Now().Format(time.RFC3339),
})
return rendered + workerTrafficBlock + workerArtifactSpec
```

`workerTrafficBlock` 和 `workerArtifactSpec` 仍由代码追加，模板不能删除这些固定输出规约。

### 2.3 WorkerName 修复

原调用没有把 Worker 名称传给 `workerSystem`，导致 `{{.WorkerName}}` 即使写在全局提示词中也没有值。

现在 `Worker.Execute` 调用：

```go
system: workerSystem(w.proxyAddr, w.caCert, w.dataDir, w.runDir, w.name, taskPrompt),
```

`server/task_llm.go:574` 给任务 Worker 注入快照回调：

```go
wk.SetTaskPromptOverride(func() string { return t.WorkerPromptOverride })
```

## 3. 数据库变更

文件：`db/schema.sql`。

### 3.1 新表 `worker_templates`

```sql
CREATE TABLE IF NOT EXISTS worker_templates (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    nkey       TEXT NOT NULL UNIQUE,
    body       TEXT NOT NULL,
    builtin    BOOLEAN NOT NULL DEFAULT false,
    is_default BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_worker_templates_default
    ON worker_templates(is_default) WHERE is_default;
```

`nkey` 用于跨环境稳定引用；`is_default` 的部分唯一索引保证最多一个默认模板。

### 3.2 任务快照列

```sql
ALTER TABLE tasks
ADD COLUMN IF NOT EXISTS worker_prompt_override TEXT NOT NULL DEFAULT '';
```

这里保存的是模板**正文**，不是模板 ID。这样模板改/删不影响旧任务。

### 3.3 场景默认模板列

```sql
ALTER TABLE task_categories
ADD COLUMN IF NOT EXISTS default_worker_template_id BIGINT NOT NULL DEFAULT 0;
```

`0` 表示场景不指定 Worker 模板，继续走全局默认模板链。

## 4. Go 数据层

### 4.1 新增 `db/worker_templates.go`

新增类型：

```go
type WorkerTemplate struct {
    ID        int64
    Name      string
    NKey      string
    Body      string
    Builtin   bool
    IsDefault bool
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

新增方法：

```text
WorkerTemplateInput.Validate
ListWorkerTemplates
GetWorkerTemplate
CreateWorkerTemplate
UpdateWorkerTemplate
DeleteWorkerTemplate
SetDefaultWorkerTemplate
DefaultWorkerTemplateBody
EnsureBuiltinWorkerTemplateSeed
```

错误类型：

```text
ErrWorkerTemplateNameConflict
ErrWorkerTemplateDefaultProtected
ErrWorkerTemplateBuiltinProtected
ErrWorkerTemplateNoDefault
```

关键行为：

- 创建和更新时检查 `nkey` 冲突。
- 默认模板禁止删除。
- 内置模板不允许通过普通 CRUD 修改或删除。
- 设置默认在事务中先清除旧默认，再设置新默认。
- `EnsureBuiltinWorkerTemplateSeed` 确保 `nkey=内置默认` 的内置模板存在且为默认。

### 4.2 修改 `db/tasks.go`

`Task` 新增：

```go
WorkerPromptOverride string `json:"worker_prompt_override,omitempty"`
```

`TaskCreateOptions` 新增：

```go
WorkerPrompt     string
WorkerTemplateID int64
```

`CreateTaskWithOptions` 将 `opts.WorkerPrompt` 写入 `worker_prompt_override`；`taskCols` 和 `scanTask` 同步增加该列，避免写入成功但读取丢失。

### 4.3 修改 `db/task_categories.go`

`TaskCategory` 新增：

```go
DefaultWorkerTemplateID int64 `json:"default_worker_template_id"`
```

`UpdateTaskCategoryPresets` 新增 `defaultWorkerTemplateID *int64` 参数，并更新/扫描 `default_worker_template_id`。

## 5. 后端 API

### 5.1 新增 `server/worker_templates.go`

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/worker-templates` | 模板列表 |
| POST | `/api/worker-templates` | 新建模板 |
| PATCH | `/api/worker-templates/{id}` | 修改名称或正文 |
| DELETE | `/api/worker-templates/{id}` | 删除模板 |
| POST | `/api/worker-templates/{id}/default` | 设为默认 |
| POST | `/api/worker-templates/validate` | 试渲染，不保存 |

保存和试渲染都调用：

```go
agent.RenderPromptTemplate("worker", body, agent.WorkerVars{
    ProxyAddr: "127.0.0.1:8788", WorkerName: "worker-preview",
    DataDir: "/data", Now: "2006-01-02T15:04:05Z07:00",
})
```

非法变量示例：

```gotemplate
{{.Goal}}
```

返回 HTTP 400，不写入数据库。

路由在 `server/server.go` 注册，全部位于认证后的 `/api` 下。

### 5.2 新建任务快照解析

`server/server.go:1549` 的 `createTaskReq` 新增：

```go
WorkerPrompt     string `json:"worker_prompt"`
WorkerTemplateID int64  `json:"worker_template_id"`
```

`createTask` 先读取场景默认值：

```go
if req.WorkerTemplateID == 0 && cat.DefaultWorkerTemplateID > 0 {
    req.WorkerTemplateID = cat.DefaultWorkerTemplateID
}
```

然后按优先级解析正文：

```go
workerPrompt := strings.TrimSpace(req.WorkerPrompt)
if workerPrompt != "" {
    // 手工覆盖优先，并验证 WorkerVars。
} else if req.WorkerTemplateID > 0 {
    tpl, err := pg.GetWorkerTemplate(req.WorkerTemplateID)
    // 模板不存在返回 400；存在则 workerPrompt = tpl.Body。
} else {
    body, err := pg.DefaultWorkerTemplateBody()
    workerPrompt = body
}
```

最终调用：

```go
s.m.CreateTaskWithOptions(req.Description, req.Goal, db.TaskCreateOptions{
    // 其他既有字段省略
    WorkerPrompt: workerPrompt, WorkerTemplateID: req.WorkerTemplateID,
})
```

`server/manager.go` 的 `Task` 和 `taskFromPG` 同步镜像 `WorkerPromptOverride`。

### 5.3 子任务规则

`server/orchestration.go` 的 `spawn_task` 没有增加需要 LLM 理解的新参数。

服务端根据 Planner 场景的自然键找同名 `task_categories`，读取该场景的 `DefaultWorkerTemplateID`，保存 Worker 正文快照；场景未配置时使用全局默认 Worker 模板。

这样可以避免 Dispatcher 传错 Worker 模板 ID，同时保持不同场景可以选择不同 Worker 模板。

## 6. 前端变更

### 6.1 新增 `web/src/components/worker-template-manager.tsx`

功能：

- 模板列表、搜索、新建、编辑、删除。
- 设为默认。
- 内置模板只读。
- 试渲染。
- 变量按钮插入 `{{.WorkerName}}`、`{{.ProxyAddr}}`、`{{.DataDir}}`、`{{.Now}}`。
- 删除前显示确认对话框。

### 6.2 新增 `web/src/components/worker-template-controls.tsx`

提供两个输入：

```text
Worker 模板（worker）
Worker 提示词覆盖（可选）
```

选择模板后把正文放入覆盖框，允许继续手改。提交时 `workerPrompt` 优先于 `workerTemplateID`。

### 6.3 修改 `web/src/app/(main)/function/tasks/page.tsx`

场景管理新增状态：

```tsx
const [draftWorkerTemplateID, setDraftWorkerTemplateID] = React.useState(0);
```

保存场景时提交：

```tsx
default_worker_template_id: draftWorkerTemplateID,
```

新建任务新增：

```tsx
const [workerTemplateID, setWorkerTemplateID] = React.useState<number | null>(null);
const [workerPrompt, setWorkerPrompt] = React.useState("");
```

选择场景时带出默认值：

```tsx
if (!workerPrompt.trim() && (cat.default_worker_template_id ?? 0) > 0) {
    setWorkerTemplateID(cat.default_worker_template_id ?? null);
}
```

高级设置加入：

```tsx
<WorkerTemplateControls
    workerTemplateID={workerTemplateID}
    onWorkerTemplateIDChange={setWorkerTemplateID}
    workerPrompt={workerPrompt}
    onWorkerPromptChange={setWorkerPrompt}
    portalContainer={sheetContentRef}
/>
```

创建任务提交：

```tsx
workerPrompt: workerPrompt.trim(),
workerTemplateId: workerTemplateID ?? 0,
```

### 6.4 浏览器验证发现的刷新问题

原实现只在页面挂载时读取场景模板：

```tsx
useEffect(() => { /* 加载模板 */ }, []);
```

这导致页面打开后新建的 Worker 模板不会出现在场景管理中。已改为每次打开场景管理重新读取：

```tsx
React.useEffect(() => {
    if (!open) return;
    api.plannerTemplates().then(setPresetTemplates).catch(() => {});
    api.workerTemplates().then(setPresetWorkerTemplates).catch(() => {});
    api.agents().then((l) => setPresetAgents(l.filter((a) => a.executor_key))).catch(() => {});
}, [open]);
```

## 7. 配置导入导出

`start.sh` 新增：

- 导出 `worker_templates_custom.jsonl`，只导出自定义模板。
- 导出场景时把 `default_worker_template_id` 转成 `default_worker_template_nkey`。
- 导入时按 `nkey` 恢复 Worker 模板和场景关联。
- 老配置没有 Worker 字段时使用 `0`，保持兼容。

新增空文件：`config-export/worker_templates_custom.jsonl`。

`config-export/manifest.txt` 和 `config-export/README.md` 已同步文件数量与说明。

## 8. 测试与结果

### 8.1 自动化测试

新增：

- `db/worker_templates_test.go`：真实 PostgreSQL 验证 CRUD、重名、默认保护、场景关联和任务快照。
- `server/worker_templates_test.go`：合法 WorkerVars、非法 Planner 变量和空正文。
- `agent/worker_prompt_test.go`：快照/默认链、WorkerName 渲染和固定尾部规约。

执行结果：

```text
ARTEX_PG_DSN=... go test -p=1 ./...       全部通过
go vet ./...                              通过
cd web && npx tsc --noEmit                通过
node --test src/lib/activity-merge.test.mjs src/lib/chat-mentions.test.mjs
                                           7/7 通过
npm run build:static                      通过，生成 31 页
git diff --check                          通过
```

### 8.2 真实 Chromium 验证

使用容器内 Chromium `156.0.8078.4` 和 Playwright，真实操作页面：

1. 打开新建任务和高级设置。
2. 打开 Worker 模板管理。
3. 输入 `{{.Goal}}`，确认试渲染拒绝。
4. 输入四个合法 WorkerVars，试渲染通过。
5. 创建模板。
6. 关闭管理页，在新建任务选择模板，确认正文带出。
7. 打开场景管理，选择该模板并创建场景。
8. 新建任务选择该场景，确认 Worker 模板自动带出。
9. 创建任务。
10. 通过 API 修改模板正文，数据库确认旧任务快照仍为原文。
11. 删除测试任务、场景和模板。

结果文件：

```text
/root/artex-worker-template-ui-result.json
/root/artex-worker-template-db-check.log
```

数据库验证结果：

```text
snapshot_ok=true
template_changed=true
category_link=true
```

截图：

```text
/root/artex-worker-template-manager.png
/root/artex-worker-template-new-task.png
/root/artex-worker-template-scenario.png
```

浏览器 `pageErrors=0`；临时任务、场景和模板清理后数量均为 0。

## 9. 部署记录

部署前备份：

```text
/root/artex-pre-worker-template-20261030-005624.dump
```

执行：

```bash
cd /root/artex-ld
docker compose up -d --build artex
```

部署后确认：

```text
artex-ld-artex-1      healthy
artex-ld-postgres-1   healthy
worker_templates      存在内置默认模板
tasks                 存在 worker_prompt_override
task_categories       存在 default_worker_template_id
```

## 10. 回滚

先备份数据库和 `/root/artex-ld`。

代码回滚本功能涉及：

```text
agent/worker.go
agent/prompt_test.go
db/schema.sql
db/tasks.go
db/task_categories.go
db/worker_templates.go
server/worker_templates.go
server/server.go
server/manager.go
server/orchestration.go
server/task_llm.go
server/task_categories.go
web/src/lib/types.ts
web/src/lib/api.ts
web/src/components/worker-template-*.tsx
web/src/app/(main)/function/tasks/page.tsx
start.sh
config-export/worker_templates_custom.jsonl
```

然后重建镜像。

仅当确认不再需要 Worker 模板数据时，才单独执行数据库清理；不要直接用旧 schema 覆盖新 schema：

```sql
ALTER TABLE task_categories DROP COLUMN IF EXISTS default_worker_template_id;
ALTER TABLE tasks DROP COLUMN IF EXISTS worker_prompt_override;
DROP TABLE IF EXISTS worker_templates;
```

删除列会永久丢失任务 Worker 快照，必须先备份。

## 11. 未验证范围

- 未使用真实 LLM 执行渗透任务；本次验证到任务创建和提示词快照层。
- 未进行多用户并发编辑测试；ARTEX 当前为单管理员模式。
- 子任务场景映射由 Go 代码和测试覆盖，未发起真实 Dispatcher LLM 调用。

## 12. 测试与实现代码逐行对照

| 测试/验证 | 测试位置 | 对应实现位置 | 覆盖点 |
|---|---|---|---|
| `TestWorkerTemplateAndTaskSnapshot` | `db/worker_templates_test.go:61` | `db/worker_templates.go:155` `CreateWorkerTemplate`、`:247` `SetDefaultWorkerTemplate`；`db/tasks.go:197` `CreateTaskWithOptions` | CRUD、重名、默认删除保护、场景关联、任务快照。 |
| `TestValidateWorkerTemplateBody` | `server/worker_templates_test.go:5` | `server/worker_templates.go:63` | WorkerVars 合法；`{{.Goal}}` 和空正文被拒绝。 |
| `TestExternalPentestDefaultMigration` | `db/worker_templates_test.go:11` | `db/scenario_template_migration.go:13`；调用 `db/db.go:163` | 旧内置默认迁移为外网渗透，ID/正文不变。 |
| `TestWorkerDomainSeeds` | `agent/worker_prompt_test.go:21` | `agent/worker_domain_templates.go:13` | 五个 Worker 场景的完整正文与公共证据规则。 |
| 任务创建快照 | 浏览器创建任务 + 数据库断言 | `server/server.go:1549` `createTask`；`server/task_llm.go:552` `agentsForTask`；`agent/worker.go:305` `workerSystem` | 场景默认、手工覆盖、模板快照、运行时优先级。 |
| 模板管理 UI | 真实 Chromium | `web/src/components/worker-template-manager.tsx:62` | CRUD、默认、试渲染、非法变量提示。 |
| 场景和建任务 UI | 真实 Chromium | `web/src/app/(main)/function/tasks/page.tsx:2752` 场景管理、`:3066` Worker 下拉、`:3628` 新建任务控件 | 场景带出 Worker 模板，模板加载刷新。 |

本次外网渗透和五场景的专门记录见 `docs/ARTEX-内置场景模板-实施记录.md`。

## 13. Agent 页面入口补齐

用户反馈 `/system/agents` 的 Worker 卡片没有 Planner 已有的“提示词：模板库 →”。根因是初版只在新建任务和场景管理中接入 `WorkerTemplateManager`，遗漏 Agent 卡片入口。

修改文件：`web/src/app/(main)/system/agents/page.tsx`

接入内容：

```tsx
import { WorkerTemplateManager } from "@/components/worker-template-manager";

onOpenWorkerTemplates={a.key === "worker" ? () => setWorkerTplMgrOpen(true) : undefined}

<WorkerTemplateManager open={workerTplMgrOpen} onOpenChange={setWorkerTplMgrOpen} />
```

Worker 卡片与 Planner 使用同样的“提示词：模板库 →”入口，点击后直接打开现有 Worker 模板管理面板，无新增 API 或数据库变更。

验证：

- `npx tsc --noEmit` 通过。
- `npm run build:static` 通过。
- Docker 重建后 `/api/health` 正常。
- 真实 Chromium 打开 `/system/agents`，确认 Worker 卡片入口数量为 1。
- 实际点击后打开“Worker 模板管理”，内置和默认徽章可见，`page_errors=[]`。
- 截图：`/root/artex-agent-worker-template-library.png`。
- 结果：`/root/artex-agent-worker-card-ui-result.json`。
