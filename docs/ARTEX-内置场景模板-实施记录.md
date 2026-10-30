# ARTEX 内置场景模板实施记录

状态：已实施、已部署到 192.168.9.104。

## 1. 需求与最终结果

本次不是增加一个空白模板名称，而是完成以下实际变化：

1. Planner 的原“内置默认”改名为“外网渗透”，正文、模板 ID、任务快照不变。
2. Worker 的原“内置默认”改名为“外网渗透”，正文、模板 ID、任务快照不变。
3. Worker 新增五个内置场景模板：内网渗透、权限提升、漏洞利用、漏洞挖掘、代码审计。
4. 新建内置场景时，同时关联同 nkey 的 Planner 和 Worker 模板。
5. 已有场景不被重启覆盖，已有任务继续使用创建时保存的正文快照。

部署后数据库和浏览器 API 均确认：Planner 6 个内置模板，Worker 6 个内置模板，两者唯一默认项都是“外网渗透”。

## 2. 文件级变更

| 文件 | 变更 |
|---|---|
| `agent/worker_domain_templates.go` | 新增。定义五个 Worker 场景正文及公共骨架复用逻辑。 |
| `agent/worker_prompt_test.go` | 新增 `TestWorkerDomainSeeds`。 |
| `db/scenario_template_migration.go` | 新增。迁移旧内置默认名称。 |
| `db/db.go` | 在数据库 schema 锁内执行迁移。 |
| `db/config.go` | Planner 内置默认常量改为“外网渗透”。 |
| `db/worker_templates.go` | Worker 内置默认常量改为“外网渗透”。 |
| `db/task_categories.go` | 修正场景首插的 Worker 模板参数和 SQL 占位符。 |
| `db/worker_templates_test.go` | 新增旧名称迁移的真实 PostgreSQL 测试。 |
| `server/assembly.go` | 启动时首插五个 Worker 场景，并在新场景上关联 Planner/Worker。 |
| `docs/ARTEX-内置场景模板-实施记录.md` | 本记录。 |

## 3. 默认模板迁移代码

### 3.1 Before

旧常量分别是：

```go
PlannerTemplateBuiltinName = "内置默认"
PlannerTemplateBuiltinNKey = "内置默认"
WorkerTemplateBuiltinName  = "内置默认"
WorkerTemplateBuiltinNKey  = "内置默认"
```

直接把常量改为“外网渗透”而不迁移旧行是错误的：旧库已经有默认行，再次首插会触发唯一默认索引冲突。

### 3.2 After

实际文件：`db/scenario_template_migration.go:10-29`。

```go
const ExternalPentestTemplateName = "外网渗透"

func migrateExternalPentestDefaultTemplates(ctx context.Context, conn *sql.Conn) error {
    const query = `
UPDATE planner_templates
SET name='外网渗透', nkey='外网渗透', updated_at=now()
WHERE builtin AND nkey='内置默认';
UPDATE worker_templates
SET name='外网渗透', nkey='外网渗透', updated_at=now()
WHERE builtin AND nkey='内置默认';`
    if _, err := conn.ExecContext(ctx, query); err != nil {
        return fmt.Errorf("rename builtin default scenario templates: %w", err)
    }
    return nil
}
```

迁移条件同时包含 `builtin` 和旧 `nkey`，所以不会重命名管理员的自定义模板。

调用位置：`db/db.go:163`，位于 `applySchemaWithRetry` 之后、`seedBuiltins` 之前，并处于数据库 schema 锁内：

```go
if err := migrateExternalPentestDefaultTemplates(context.Background(), conn); err != nil {
    return err
}
```

迁移失败会阻止数据库初始化继续执行，不会忽略冲突后制造第二个默认模板。

新常量位置：

- `db/config.go:1151-1152`：Planner 默认名称和 nkey。
- `db/worker_templates.go:19-20`：Worker 默认名称和 nkey。

## 4. Worker 场景提示词生成代码

实际文件：`agent/worker_domain_templates.go:1-82`。

数据结构：

```go
type WorkerDomainSeed struct {
    NKey string `json:"nkey"`
    Body string `json:"body"`
}
```

生成方式不是只保存一小段场景补充说明，而是把场景段插入完整 `workerDefaultTmpl`，再将完整正文保存到 `worker_templates.body`：

```go
body := strings.Replace(
    workerDefaultTmpl,
    "**边发现边写回**",
    sections[name]+"\n\n**边发现边写回**",
    1,
)
seeds = append(seeds, WorkerDomainSeed{NKey: name, Body: body})
```

这样有两个结果：

- 公共的 webshell、投放物、流量、证据和分类规则只维护一份源代码骨架。
- 数据库里仍是完整可编辑提示词，不依赖运行时拼接另一张表。

公共骨架位置：`agent/worker.go:236` 的 `workerDefaultTmpl`；其中漏洞分类和证据要求在 `agent/worker.go:258`。

## 5. 五个 Worker 场景完整新增提示词

以下内容逐字来自 `agent/worker_domain_templates.go`。它们会插入公共骨架中，不是替代公共规则。

### 5.1 内网渗透

```text
## 当前场景：内网渗透
- 只从父任务登记的立足点和已授权直连范围开始。先识别主机、网段、服务、凭据和可达关系，再决定是否利用。
- 每次横向动作都写清源主机、目标主机、协议和证据来源；直连可达不等于获得授权。
- 发现可复用凭据、配置或拓扑事实时 record_fact；关键 HTTP 和代理流量沿用 stream_id 与 tool_call_id。
- 获得新主机控制权后，按实际证据登记 getshell、webshell 或获取目标权限，不把端口开放当作立足点。
```

### 5.2 权限提升

```text
## 当前场景：权限提升
- 从已验证的低权限上下文开始，先记录当前用户、主机、权限边界和现有凭据，再检查配置、令牌、服务和已知提权路径。
- 优先可读、可回滚、低噪声的验证；禁止破坏数据、拒绝服务、关闭审计或留下持久化后门。
- 每次权限变化都用原上下文与新上下文的对照证据证明。只读取到敏感配置但没有获得权限时，登记信息泄露而不是获取目标权限。
- 实际获得更高权限账户或命令执行上下文后登记获取目标权限；另有反弹或命令 Shell 时按 getshell 登记。
```

### 5.3 漏洞利用

```text
## 当前场景：漏洞利用
- 先固定目标、漏洞前提、受影响组件和授权边界，再设计最小化 PoC。逐步记录请求、工具、版本和观察结果。
- 先证明可控性与影响，再考虑扩大利用。获得所需证据后停止，不为了展示效果破坏数据或扩大影响。
- 上传 Web 后门必须验证可访问或可连接，并遵守 godzilla 登记纪律；只有文件写入成功不能登记 webshell。
- 每次利用结果都关联原始流量或工具调用；失败时保留排除证据，不把扫描器告警直接写成漏洞。
```

### 5.4 漏洞挖掘

```text
## 当前场景：漏洞挖掘
- 围绕当前目标提出少量可证伪假设，逐个改变输入、身份、状态或协议条件，并保留对照结果。
- 优先覆盖同类输入面、相邻接口和不同角色，不偏离当前目标做无边界的全网扫描。
- 对候选漏洞先排除缓存、错误配置、前端展示和偶然差异，再确认可复现性与实际影响。
- 只有验证完成的事实才进入 report_finding；研究过程、排除路径和待确认假设使用 record_fact。
```

### 5.5 代码审计

```text
## 当前场景：代码审计
- 先识别入口、数据来源、敏感 sink、鉴权与过滤链路，再沿真实调用关系证明可达性；不因关键词命中就报告漏洞。
- 记录文件、函数、关键语句和控制流。能用运行时验证时，继续给出最小化验证和对应流量；不能运行时验证时明确证据缺口。
- 区分源到 sink 的理论路径、可利用路径和已验证漏洞。只有满足任务证据门槛的结果才登记 finding，其余写审计事实。
- 不修改目标源码，不执行构建脚本中的未知命令；需要运行测试或动态验证时先遵守任务授权和 Worker 证据纪律。
```

## 6. 启动首插与场景关联

`server/assembly.go:232-239`：

```go
for _, seed := range agent.WorkerDomainSeeds() {
    if inserted, err := pg.SeedWorkerTemplateIfEmpty(
        seed.NKey, seed.NKey, seed.Body, false,
    ); err != nil {
        log.Printf("[prompts] seed worker 场景模板 %s 失败: %v", seed.NKey, err)
    } else if inserted {
        log.Printf("[tpl] worker 场景模板首插：%s", seed.NKey)
    }
}
```

首插语义由 `db/worker_templates.go:289` 的 `SeedWorkerTemplateIfEmpty` 实现。重启不会用代码正文覆盖管理员后续编辑。

### 6.1 场景首插缺陷及修复

检查时发现旧 `SeedTaskCategoryPresetIfEmpty` 已有 `default_worker_template_id` 列，但函数参数和 VALUES 不完整。

修复位置：`db/task_categories.go:204-216`。

```go
func (d *DB) SeedTaskCategoryPresetIfEmpty(
    name string,
    reviewEnabled bool,
    plannerTemplateID, workerTemplateID int64,
    seedFirstIntent bool,
    seedExecutor string,
) (bool, error) {
    // ...
    res, err := d.Exec(`INSERT INTO task_categories(
        name, nkey, default_review_enabled,
        default_planner_template_id, default_worker_template_id,
        seed_first_intent, seed_executor)
VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (nkey) DO NOTHING`,
        name, nkey, reviewEnabled, plannerTemplateID,
        workerTemplateID, seedFirstIntent, seedExecutor)
    // ...
}
```

调用位置：`server/assembly.go:253-263`。先分别查询 Planner 和 Worker 模板，再首插分类；`ON CONFLICT DO NOTHING` 保证已有分类不被覆盖。

## 7. 测试与实现逐行对照

以下行号是本次完成后的实际源文件行号，不是文档示例编号。

| 测试/验证 | 测试文件和行号 | 对应实现文件、函数和行号 | 验证内容 |
|---|---|---|---|
| `TestWorkerDomainSeeds` | `agent/worker_prompt_test.go:21` | `agent/worker_domain_templates.go:13` `WorkerDomainSeeds`；公共正文 `agent/worker.go:236` | 五场景顺序、场景段、专属规则、`report_finding`、公共写回锚点。 |
| `TestWorkerPromptClassifiesVerifiedFootholds` | `agent/worker_prompt_test.go:8` | `agent/worker.go:258` `workerDefaultTmpl` 中 vulnclass 段 | getshell、webshell、获取目标权限和有回显证据要求未被场景模板破坏。 |
| `TestExternalPentestDefaultMigration` | `db/worker_templates_test.go:11` | `db/scenario_template_migration.go:13`；调用 `db/db.go:163` | 真实 PostgreSQL 中旧“内置默认”改名；ID 和正文保持不变。 |
| `TestWorkerTemplateAndTaskSnapshot` | `db/worker_templates_test.go:61` | `db/tasks.go:197` `CreateTaskWithOptions`；`db/task_categories.go:121` | Worker 模板 CRUD、默认保护、场景关联、任务正文快照。 |
| `TestValidateWorkerTemplateBody` | `server/worker_templates_test.go:5` | `server/worker_templates.go:63` | 四个 WorkerVars 合法，Planner 专用变量和空正文被拒绝。 |
| 全量 Go 回归 | `go test -p=1 ./...` | 所有 Go 包 | 在独立 PostgreSQL 上全部通过。 |
| 静态检查 | `go vet ./...`、`npx tsc --noEmit` | Go/TypeScript 全量 | 全部通过。 |
| Windows 浏览器 MCP | 结果文件见第 8 节 | `GET /api/planner-templates`、`GET /api/worker-templates` | 19 项断言通过。 |

## 8. Windows 浏览器 MCP 验证

验证路径：

```text
本机 MCP 客户端
  → http://127.0.0.1:40326/mcp
  → Windows ShardX Launcher 127.0.0.1:40325
  → 真实临时 Chrome
  → http://192.168.9.104:8787
```

没有把 ShardX 配进 ARTEX，没有记录访问令牌或登录密码。临时浏览器 profile 在验证后停止并删除。

浏览器完成真实登录后，在页面上下文执行同源 `fetch`，检查：

- 两类模板 HTTP 状态均为 200；
- 每类都有六个内置模板；
- 每类唯一默认模板都是“外网渗透”；
- Worker 五场景正文包含对应 `## 当前场景：...`；
- Worker 正文仍包含 `report_finding` 和 `**边发现边写回**`。

结果：

```json
{"ok":true,"checks":19,"planner":6,"worker":6}
```

证据文件：

```text
C:\Users\Administrator\Downloads\artex-docs\artex-scenario-browser-result.json
```

## 9. 自动化命令和日志

```bash
ARTEX_PG_DSN=... go test ./agent -run 'TestWorkerPromptClassifiesVerifiedFootholds|TestWorkerDomainSeeds' -v
ARTEX_PG_DSN=... go test ./db -run 'TestExternalPentestDefaultMigration|TestWorkerTemplateAndTaskSnapshot' -v
ARTEX_PG_DSN=... go test -p=1 ./...
go vet ./...
cd web && npx tsc --noEmit && npm run build:static
```

日志：

```text
/root/artex-scenario-templates-go-test.log
/root/artex-scenario-templates-go-vet.log
/root/artex-scenario-templates-web-build.log
```

## 10. 部署和数据库证据

```text
备份：/root/artex-pre-scenario-templates-20261029-032154.dump
部署：docker compose up -d --build artex
结果：artex-ld-artex-1 / artex-ld-postgres-1 均 healthy
```

部署后核验：

- Planner 原默认行仍为 ID 1，改名外网渗透，正文未改。
- Worker 原默认行仍为 ID 1，改名外网渗透，正文未改。
- Planner 和 Worker 均有六个内置模板。
- 本次环境 Worker 五场景首插 ID 为 17–21；文档和代码不依赖这些数字 ID。

## 11. 回滚

代码回滚前，先明确数据如何处理：

1. 新任务正文是快照，回滚模板不会修改已有任务。
2. 如需回到旧代码，删除五个 `builtin=true AND is_default=false` 的 Worker 场景模板前，先把引用它们的场景默认改为 0 或其他模板。
3. 如确需恢复旧名称，只把原内置默认行的 name/nkey 改回“内置默认”；不要批量修改自定义模板。
4. 优先使用 `/root/artex-pre-scenario-templates-20261029-032154.dump` 做数据库级恢复，再部署旧镜像。

## 12. 外网渗透场景遗漏修正

### 原因

第一版只新增了外网渗透 Planner/Worker 模板，没有把它加入 `catScenarios`。模板库存在模板，不代表 `task_categories` 里存在场景，因此场景管理看不到。这是实现遗漏，不是前端显示问题。

### Before

`server/assembly.go` 的 `catScenarios` 只有内网渗透、权限提升、漏洞利用、漏洞挖掘、代码审计五项。

### After

实际代码：`server/assembly.go:249`。

```go
catScenarios := []struct {
    name   string
    tplKey string
    review bool
}{
    {"外网渗透", "外网渗透", true},
    {"内网渗透", "内网渗透", true},
    {"权限提升", "权限提升", true},
    {"漏洞利用", "漏洞利用", true},
    {"漏洞挖掘", "漏洞挖掘", false},
    {"代码审计", "代码审计", false},
}
```

同一个循环已经在 `server/assembly.go:255-263` 按 nkey 解析 Planner/Worker 模板，所以新增这一项后会同时写入两个默认模板 ID。

### 回归测试与实现对应

| 测试 | 测试位置 | 生产实现 | 断言 |
|---|---|---|---|
| `TestSeedPromptsIncludesExternalPentestScenario` | `server/scenario_templates_test.go:9-45` | `server/assembly.go:194` `seedPrompts`；场景项 `:249` | 删除测试库中的外网场景后连续执行两次首插；只生成一个场景，review=true，Planner/Worker nkey 都是外网渗透。 |

执行命令：

```bash
ARTEX_PG_DSN=... go test ./server -run TestSeedPromptsIncludesExternalPentestScenario -v
```

正式 API 已返回外网渗透场景，`default_review_enabled=true`，两个模板 ID 均为 1。部署前备份：`/root/artex-pre-external-scenario-20261029-040702.dump`。
