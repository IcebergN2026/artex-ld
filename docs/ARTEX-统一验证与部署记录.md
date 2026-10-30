# ARTEX 统一验证与部署记录

状态：已完成验证并部署到 192.168.9.104；源代码尚未提交 Git。

## 部署

- 部署前 PostgreSQL 备份：`/root/artex-pre-feature-deploy-20261028-232634.dump`，权限 0600。
- 执行 `docker compose up -d --build artex`；修复浏览器发现的问题后再次重建。
- `artex-ld-artex-1` 和 `artex-ld-postgres-1` 最终均为 healthy。
- `GET http://127.0.0.1:8787/api/health` 返回 `{"ok":true,"service":"artex","version":"dev"}`。
- 仅把 `config-export` 中 trace_cleaner 的当前提示词同步到现有 Agent，不覆盖其他用户配置。
- 数据库确认：Dispatcher 使用 nkey、旧硬编码 ID 为 0、拓扑两张表存在。

## 自动化验证

| 检查 | 结果 | 日志 |
|---|---|---|
| `ARTEX_PG_DSN` 下 `go test -p=1 ./...` | 通过 | `/root/artex-final-go-test-20261029.log` |
| `go vet ./...` | 通过 | `/root/artex-final-go-vet-20261029.log` |
| `npx tsc --noEmit` | 通过 | 命令行 |
| 两个既有 Node 测试文件 | 通过 | `/root/artex-final-web-test-20261029.log` |
| `npm run build:static` | 通过，31 页 | `/root/artex-web-build-20261029.log` |
| `git diff --check` | 通过 | 命令行 |

复跑时既有 `TestCompanyScopeAttribution` 因测试库中历史 company/scope 未清理而失败；重建独立测试 PostgreSQL 后，全量测试通过。未为绕过该问题修改业务代码或测试断言。

## 真实 Chrome / CDP 验证

使用容器内 Playwright `chromium.connectOverCDP`，生成仅用于测试的短期 JWT，不输出令牌，不使用生产用户凭据。

- `/function/assets/`：宿主 Tab、六列、服务/接口聚合、行筛选、取消筛选、同页发现列表、760px 窄屏横向滚动均通过。
- `/function/tasks/detail`：内网拓扑、公网入口、内网节点、节点 JSON、关系保存、备注历史均通过；抽屉无原始命令展示。
- 浏览器无未捕获运行时异常。
- 截图：`/root/artex-assets-ui.png`、`/root/artex-assets-ui-narrow.png`、`/root/artex-topology-ui.png`。

## 浏览器发现的问题与修复

初次验证发现裸 IP 的 `subdomains` 被序列化为 `null`，宿主页面读取数组时崩溃。

- 修改 `db/host_assets.go`：构造集合时使用非 nil 空切片，API 统一返回 `[]`。
- 修改 `db/host_assets_test.go`：增加裸 IP 的非 nil 断言。
- 聚焦测试、全量测试和真实浏览器复验均通过。

## 测试数据清理与边界

- 临时任务、2 条发现、8 项资产、探索和企业均在同一事务中删除。
- 清理后临时任务/资产/企业计数均为 0；临时 JWT 文件和浏览器脚本已删除。
- 未配置 LLM，因此没有执行真实模型的漏洞派发或 trace_cleaner 对话清理；提示词行为仍需在配置模型后人工验收。
- 拓扑没有 Agent 自动执行进度面板，符合本期需求边界。

## Worker 模板追加验证（2026-10-30）

### 代码与测试

```bash
# 独立 PostgreSQL，DSN 不写入仓库
ARTEX_PG_DSN=... go test ./db -run TestWorkerTemplateAndTaskSnapshot -v
go test ./server -run TestValidateWorkerTemplateBody -v
ARTEX_PG_DSN=... go test -p=1 ./...
go vet ./...
cd web
npx tsc --noEmit
node --test src/lib/activity-merge.test.mjs src/lib/chat-mentions.test.mjs
npm run build:static
```

结果：真实 PostgreSQL 测试通过，全量 Go/vet 通过，Node 7/7 通过，静态构建 31 页通过。

### 浏览器

真实 Chromium `156.0.8078.4` 验证：

- Worker 模板 CRUD、设默认和试渲染；
- `{{.Goal}}` 被拒绝，四个 WorkerVars 合法；
- 场景管理选择 Worker 模板；
- 新建任务从场景带出模板；
- 任务保存 Worker 正文快照；
- 修改模板后旧任务快照不变；
- 浏览器 `pageErrors=0`。

浏览器发现“场景管理只在首次挂载加载模板”，已把 `CategoryManagementSheet` 的加载 effect 改为依赖 `open`，每次打开刷新 Planner/Worker 模板和 Agent。

证据：

```text
/root/artex-worker-template-ui-result.json
/root/artex-worker-template-db-check.log
/root/artex-worker-template-manager.png
/root/artex-worker-template-new-task.png
/root/artex-worker-template-scenario.png
```

数据库断言：`snapshot_ok=true`、`template_changed=true`、`category_link=true`。测试任务、场景、模板删除后均为 0。

### 部署

```text
备份：/root/artex-pre-worker-template-20261030-005624.dump
命令：docker compose up -d --build artex
结果：artex/postgres healthy；worker_templates 内置默认模板 1 条。
```

独立测试容器 `artex-test-pg` 和临时 DSN 文件已在最终验证后删除。

## 内置场景模板追加验证

### 实现范围

- Planner/Worker 原内置默认迁移为外网渗透，保留 ID、正文和任务快照。
- Worker 新增内网渗透、权限提升、漏洞利用、漏洞挖掘、代码审计五个完整模板。
- 新内置场景首插时关联同 nkey 的 Planner/Worker 模板，已有场景不覆盖。

### 测试与代码位置

| 测试 | 测试位置 | 实现位置 |
|---|---|---|
| `TestWorkerDomainSeeds` | `agent/worker_prompt_test.go:21` | `agent/worker_domain_templates.go:13` |
| `TestExternalPentestDefaultMigration` | `db/worker_templates_test.go:11` | `db/scenario_template_migration.go:13`，调用 `db/db.go:163` |
| 场景首插关联 | 全量 DB 测试与部署后 SQL | `db/task_categories.go:204`、`server/assembly.go:232-263` |
| 全量回归 | `ARTEX_PG_DSN=... go test -p=1 ./...` | 全部 Go 包 |

日志：

```text
/root/artex-scenario-templates-go-test.log
/root/artex-scenario-templates-go-vet.log
/root/artex-scenario-templates-web-build.log
```

### Windows 浏览器 MCP

真实临时 Chrome 登录后，在浏览器上下文读取两个模板 API。最终 19 项断言通过：

```json
{"ok":true,"checks":19,"planner":6,"worker":6}
```

证据：`C:\Users\Administrator\Downloads\artex-docs\artex-scenario-browser-result.json`。

复杂 UI 点击脚本的多次选择器等待失败不计为产品测试失败；最终采用浏览器内 API 验证，没有为绕过脚本问题修改应用。

### 部署

```text
备份：/root/artex-pre-scenario-templates-20261029-032154.dump
命令：docker compose up -d --build artex
结果：artex/postgres healthy；Planner/Worker 各 6 个内置模板，外网渗透为唯一默认。
```

详细代码和五个 Worker 场景完整提示词见 `docs/ARTEX-内置场景模板-实施记录.md`。

## 资产默认页、拓扑显示备注与授权范围（2026-10-29 最终复验）

### 自动化

```text
/root/artex-final-fix-go-test.log    ARTEX_PG_DSN 下 go test -p=1 ./... 全部通过
/root/artex-final-fix-go-vet.log     go vet ./... 通过
/root/artex-final-fix-web-build.log  TypeScript 与 build:static 通过
/root/artex-final-fix-deploy.log     Docker 重建，最终 healthy
```

### Windows 浏览器

`artex-final-fix-browser-result.json` 最终 `ok=true`，五项检查：

```text
assets-default-host
assets-six-columns
topology-display-label-api
topology-display-label-visible
topology-relation-fields-preserved
```

截图：

```text
C:\Users\Administrator\Downloads\artex-docs\artex-assets-default-host.png
C:\Users\Administrator\Downloads\artex-docs\artex-topology-display-label.png
```

拓扑临时显示备注已恢复为空，关系字段保持原值。测试使用短期 JWT，没有依赖用户密码；JWT 已过期。独立 PostgreSQL 测试容器和 DSN 文件已删除。

### 授权边界

正式任务 `#7` 保持 done，未重新执行。goal、exploration 和 allow/deny 均限制为仅 `192.168.9.101`；其他 `192.168.9.x` 禁止，另一网卡只允许记录配置。Dispatcher/内网 Planner/Worker 数据库提示词已通过一次性迁移生效。

详细记录：

- `docs/ARTEX-资产宿主列表-实施记录.md` 第 13 节。
- `docs/ARTEX-内网渗透拓扑-实施记录.md` 第 16 节。
- `docs/ARTEX-内网任务授权范围修正-实施记录.md`。

## Planner / Worker 内置模板直接编辑

详细记录：`docs/ARTEX-内置模板直接编辑-实施记录.md`。

- 内置正文可直接编辑、试渲染和保存；内置名称/nkey 与删除保护不变。
- 新增 `TestBuiltinPlannerTemplateBodyEditable` 和 `TestBuiltinWorkerTemplateBodyEditable`，真实 PostgreSQL 验证正文、身份、改名和删除行为。
- 全量 Go 测试、go vet、TypeScript、build:static 通过，Docker healthy。
- 真实 Chrome 对 Worker 和 Planner 均完成编辑、保存、刷新持久化和恢复。

证据：

```text
artex-worker-builtin-editable.png
artex-worker-builtin-edit-result.json
artex-planner-builtin-editable.png
artex-planner-builtin-edit-result.json
```

## 漏洞派发立足点门槛

详细记录：`docs/ARTEX-漏洞派发立足点门槛-实施记录.md`。

- 仅 `getshell/webshell/reverse_shell + critical/high` 且具有真实立足点证据时派发。
- RCE、仅上传木马、file_upload/deserialization 不再直接触发派发。
- 全量测试和 Docker 部署通过；正式库旧触发器已显式更新。

## Worker 投放事实登记

详细记录：`docs/ARTEX-Worker投放事实登记-实施记录.md`。

- 默认 Worker 和五个场景变体均包含 `[投放] → record_fact` 强制契约。
- 一次性迁移已更新六个内置模板及全局 worker 当前正文；历史任务快照不改。
- 全量 Go 测试、go vet 和 Docker 健康检查通过。
- 配置导出与源码默认完全一致。

## Goal 与外网 Planner 立足点提示词

详细记录：`docs/ARTEX-Goal与Planner立足点提示词-实施记录.md`。

- Goal 新增强 Shell 意图、弱权限意图、检测/验证意图和扩展中英文关键词。
- 仅外网 Planner 内置模板新增 getshell 立足点证据验收；其余五套领域模板未改。
- 普通 RCE、文件上传、反序列化和 SQL 注入验证不被强制升级为 getshell。
- 正式库 Goal 通过新版本切换，外网模板正文保留并插入规则；历史任务快照未改。
- `TestGoalAndExternalPlannerFootholdSemantics`、`TestFootholdPromptChangeIsLimitedToExternalPlanner` 通过。
- 全量 Go 测试、go vet、git diff --check 和 Docker 健康检查通过。
- 本次没有增加 `prove_goal` / `goal_met` 硬校验，也没有执行真实 LLM/目标测试。

## 内网 Planner 低噪声提示词

详细记录：`docs/ARTEX-内网Planner低噪声提示词-实施记录.md`。

- 移除固定域控/数据读取/网段达成判据，以及 `whoami/hostname` 固定命令要求。
- 新增低噪声、最少必要操作和已有证据复用规则。
- WebShell、C2 和其他 MCP 均为可选路径；无 MCP 不阻塞其他漏洞利用路径。
- 仅更新内网 Planner 内置模板，Dispatcher、Worker、触发器、其他模板和历史快照未改。
- `TestInternalPlannerLowNoiseAndOptionalMCP`、全量 Go 测试、go vet 和 Docker 健康检查通过。

## 任务执行模式与 Dispatcher 双模式

详细记录：`docs/ARTEX-任务执行模式与Dispatcher优化-实施记录.md`。

- 新增 `foothold` 与 `vulnerability_only` 输入识别；普通“验证 RCE”仍走默认模式。
- Goal 和 Planner 只在明确仅漏洞任务中追加对应验收规则，不强制 Shell。
- Finding 触发上下文向 Dispatcher 传递执行模式。
- Dispatcher 同时支持立足点与仅漏洞模式；description/goal 的最新分工见下节，已取代本阶段早期的短标题约定。
- 默认模式维持立足点门槛；仅漏洞模式允许已确认的 RCE/上传/反序列化等高危档进入漏洞利用子任务。
- Worker 会话复用已按用户要求在部署前全部撤回，正式库无 `[会话]` 或迁移记录。
- 执行模式、Goal/Planner、Dispatcher、触发上下文和导出配置的聚焦测试通过。
- 全量 Go 测试、go vet、git diff --check 和 Docker 健康检查通过。
- 未做真实 LLM、目标请求或子任务派发测试；历史任务快照未改。

## 运行中子任务 Finding 追加与 description/goal 调整

详细记录：`docs/ARTEX-运行中子任务Finding追加-实施记录.md`。

- 新增 `append_finding_to_task`，将当前 Finding 的内容和证据补充到同血缘、运行中的内网渗透子任务。
- 复用 AddHint/Planner 唤醒链，使用持久 Finding 标记去重；跨血缘、非内网和非运行状态均拒绝。
- Finding 触发事件和消息现在携带全局 `finding_id`。
- Dispatcher 的 description 改为完整七段执行说明；goal 写内网最高权限、关键管理主机横向或仅漏洞交付目标。
- 正式提示词升级到版本 8，存量库工具绑定已验证；历史任务和旧提示词版本未覆盖。
- `TestAppendFindingToTaskRunningChild`、触发上下文和提示词契约测试通过；全量 Go 测试、go vet、配置导出和 Docker 健康检查通过。
- 未做真实 LLM 自主派发或真实目标操作。

## Dispatcher 子任务短名称

详细记录：`docs/ARTEX-Dispatcher子任务命名-实施记录.md`。

- `spawn_task` 新增 `name` 并写入已有 `tasks.name`。
- 内网任务名格式为 `内网渗透 · <立足点 host>`；七段 description 不再兼任任务名。
- 正式 Dispatcher 提示词升级到版本 9；历史任务和旧提示词版本未覆盖。
- 提示词契约测试、全量 Go 测试、go vet 和 Docker 健康检查通过。
- 未执行真实 LLM 派发或目标操作。

## 派发范围与内网拓扑场景

详细记录：`docs/ARTEX-派发范围与内网拓扑场景-实施记录.md`。

- Dispatcher 保留父任务证据继承，不复制父 `task_constraints`，不默认生成单资产限制。
- 子任务范围按自身目标重新生成；用户明确授权边界和禁止项仍为上限。
- 任务全链路新增 `category_nkey`，内网场景重命名保留稳定 `nkey`。
- 外网任务不显示、不请求内网拓扑；三个拓扑 API 对非内网任务统一返回 404。
- 外网父任务派生的内网子任务正常显示并访问拓扑。
- 正式提示词升级到版本 10；未新增数据库表列，未删除历史拓扑数据。
- 场景键、DTO、分类重分配、三个 API 和提示词契约测试通过；全量 Go 测试、go vet、TypeScript、静态构建通过。
- 真实 Chrome 验证外网请求数为 0、三个 API 为 404、内网子任务请求和显示正常，结果 `pass=true`。
- Docker 健康检查通过；未执行真实 LLM 自主派发或目标操作。

## 内网子任务宽泛描述与信息收集

详细记录：`docs/ARTEX-内网子任务宽泛描述与信息收集-实施记录.md`。

- 移除 Dispatcher 中用户指出的可达性授权限制文案，内网 Goal 先全面收集主机、容器、网络、服务与凭据，再最高权限和横向。
- 内网 Planner 新增开局强制信息收集及“没有收集事实或进行中意图不得结束”的提示词门槛。
- 正式 Dispatcher 提示词升级到版本 11；内网 Planner 按稳定 `nkey` 更新。
- 全量 Go 测试、go vet 和 Docker 健康检查通过。
- 实际 #21 子任务并未完成，两个信息收集意图仍在运行；未修改其历史快照或任务状态。

## 拓扑移除虚拟未建立关系节点

详细记录：`docs/ARTEX-拓扑移除虚拟未建立关系节点-实施记录.md`。

- 无入口证据时不再生成 `entry:unknown` 虚拟根、合成边和“未建立关系”节点。
- 真实节点保留并独立布局，真实入口及人工/Agent 关系不受影响。
- 全量 Go 测试、go vet、TypeScript、静态构建和 Docker 健康检查通过。
- 真实任务 #21 API 返回 1 个真实节点、0 条边和空根；真实 Chrome 验证无虚拟节点、无页面错误，`pass=true`。

## 2026-10-29：Finding 资产全局去重与同资产追加复活

- 新增 `task_dispatch_targets` 规范化资产唯一约束，内网 `spawn_task` 强制 `finding_id`，阻止跨父任务及并发重复。
- `check_internal_asset_task` 按 Finding 资产查询；旧任务按 finding ID 回填，最老任务优先。
- `append_finding_to_task` 已取消直接血缘和 running 限制；同资产内网任务均可接收 Finding。
- 终态且无开放 Goal 时新增幂等跟进 Goal，经统一 admission 恢复任务，保留 Review 流程。
- 全量 Go 测试、go vet、diff check 通过；Docker healthy；正式 Dispatcher prompt version=13。
- 详细记录：`docs/ARTEX-Finding资产派发与追加-实施记录.md`。

## 2026-10-29 拓扑证据关系与 Review 右侧卡片原型

- 删除拓扑根节点自动星形边；Agent 仅可凭本任务 finding/fact 登记关系，内网 Worker 当前模板已更新。
- 全量 Go 测试、go vet、Docker 部署通过；任务 #21 从 9 条无证据边变为 0 条，真实 Chrome 验证 10 节点/0 边。
- 拓扑详细记录：`docs/ARTEX-拓扑证据关系修复-实施记录.md`。
- Review 最终原型直接复用原版任务详情 DOM/CSS，已验证桌面右卡、命令详情、允许、拒绝和 390px 移动端；见 `docs/ARTEX-Review右侧审核卡片-原型说明.md`。
- 用户确认保留拓扑修复，不回滚。

## 2026-10-29 Review 聊天分栏生产实现

- 任务开启审核时，聊天主区域左侧 30% 显示真实 Review 待审队列，右侧 70% 保留聊天；小屏聊天在上、审核在下。
- 支持多意图数字切换、上一项/下一项、通过和带理由驳回，复用现有 reviewProposed/approve/reject API 与 CAS。
- TypeScript、静态构建、diff check 和 Docker 健康检查通过。
- 真实 Chrome 使用临时任务 #23 验证 3 项审核、通过、驳回及移动端，pass=true；临时数据已清理。
- 详细记录：`docs/ARTEX-Review聊天分栏审核-实施记录.md`。
