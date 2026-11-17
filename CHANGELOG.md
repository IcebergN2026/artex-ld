# artex-ld 更新日志

本项目的重要变更记录在此文件中，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

版本说明：`artex-ld` 从 `0.1.2` 起使用独立版本序列。Release 名称为 **artex-ld 0.1.6**，Git Tag 为 **v0.1.6**。文末 `0.3.15` 及更早内容是上游 ARTEX 历史，不属于 `artex-ld` 的版本号序列。

## [Unreleased]

### 任务轨迹(Trajectory)Tab

- 任务详情页新增「轨迹」tab:单一时间线合并展示任务下所有 agent(主 Agent / 规划 / 各 Worker)的思考、工具调用与结果、文本与终答,SSE 实时追加 + 滚动到顶向上翻历史分页,支持按 agent(带步数 chips)与步骤类型(思考/文本/工具/结果/系统)本地筛选。计划与落地记录见 `docs/任务轨迹Tab-计划与落地.md`。
- 轨迹可视化增强:① 右侧 minimap 密度导航条——每步压成 3px 色块(颜色=步骤类型),视口指示框随滚动同步,点击色块跳转定位并打开详情,被筛选步骤变暗但分布可见,运行中底部显示实时边缘线;② 步骤详情抽屉——点击时间线行或 minimap 色块打开,展示思考全文/工具入参(JSON 美化)+ 配对输出/终答全文,元信息含时间、执行者、所属意图、token;③ 意图归属条——基于 `Activity.intent_id` 聚合「哪个 Worker 执行哪个意图」(标题、状态、步数),点击按意图过滤时间线;Transcript 各分组根元素新增 `data-seq` 锚点(支撑跳转与选中高亮),合并视图步骤行新增 `#intent_id` 归属标签。零后端改动,全部基于既有 `session=all` 历史、`/exploration/intents` 与单步 `activity/{seq}` 接口。
- 后端 `GET /api/exploration/activity/history` 的 `session` 参数新增 `all`（零值 `ActivitySessionFilter` = 整个任务全部 lane 不过滤）；既有会话键（`main`/`main:N`/`plan`/`intent:<id>`）行为不变。
- `web/src/components/transcript.tsx` 工具配对键由 `tool_use_id` 改为 worker 作用域（合并多 agent 时间线时跨 worker 重复 id 不再错配）；`AnswerBlock`/`UserRow` 在混排视图显示 worker chip。
- 修复 `createTask` 无界请求正文：新增 `maxCreateTaskBodyBytes`（1 MiB）并接入 `decodeJSON`（空 body 400、超限 413），同时消除 `server/jsonbody_test.go` 引用未定义常量导致的测试包编译失败。

### 资产凭据的发现与认证关系

- 新增 `asset_credentials`（凭据身份，按 cred_type/username/domain/secret 唯一）与 `asset_credential_links`（凭据与资产的 `discovered`/`verified` 关系）两张表；存量 `assets.auth` 由一次性迁移（`asset_credential_auth_backfill_v1`）展开为各原始资产的 `discovered` 关系，历史列保留但代码不再读写。
- Worker 新增 `record_credential` 与 `mark_credential_verified` 两个写工具：登记任务范围内资产的凭据发现、把已发现凭据标记为在任务内任意资产（含跨主机）认证成功；两次写入均校验任务范围与凭据可见性，返回不含 secret。
- 资产读取（GetByIDs/DSL/按任务/公司/类型及 scope 查询）统一从新表批量回填脱敏凭据，`Asset.Auth` 每项含 `id/relation/target` 等字段，secret 不再经任何 API 或工具输出返回。
- 资产页“认证”列改为“发现 n / 已认证 m”汇总，点击打开当前资产凭据 Sheet，分“本机发现/本机认证通过”两组展示；任务归档/恢复随资产一并保存并按唯一键合并恢复凭据关系，资产删除级联清理关系与孤儿凭据。实施细节见 `dev/assets-credentials-implementation.md`。

### MCP 命令拦截

- 将 `ExecuteExtraTool` 加入 `intercept_enabled_tools` 后，经该入口执行的 MCP 与延迟自定义工具调用可纳入统一拦截链：按 `tool_name` 或完整 `tool_input` 匹配字符串/正则规则，支持 `allow`、`deny`、`ask` 人工审批，以及规则未命中时的可选 LLM 兜底判定和审计记录。
- 默认拦截启用集仍不包含 `ExecuteExtraTool`，LLM 判官也保持默认关闭、`fail_action=allow`；需要 MCP 纳审时，须通过 `PUT /api/intercept/tool-config` 显式将该工具加入 `enabled_tools` 并配置相应规则，不能把本项理解为默认强制拦截全部 MCP 调用。

## [0.1.6] - 2026-11-14

### 全局 Agent 控制与开关修复

- `planner_controlled=false` 改为读取 Agent 当前配置并对所有 custom intent 生效，不再限定 `origin=seed` 或依赖 `planner_policy` 快照；已有 intent 与非首个触发路径均受同一门控，Agent 缺失时仍按 `true` 兼容。
- 自定义执行器新增 `list_intents` 与 `claim_executor_intent` 工具：Agent 先查询 proposed，再按服务端 executor_key 自行 CAS 为 running；无匹配直接退出且不改状态，真正完成后才写 done。Engine 不再为 pre_agent 预领取，只有自领成功后才恢复后续普通 Worker；领取冲突、executor 不匹配和重复领取均被拒绝。声明 executor_key 的对话型 Agent 自动获得这两个任务工具，普通 Worker 不注入。
- `pre_agent` 任务按触发配置选择唯一入口：未启用 `on_task_create` 时由 `customExecutorLoop` 提供候选；已启用时由对话型 Agent 自领，Engine 不再重复启动 customExecutorLoop。两条路径都保持普通 Worker 互斥，直到对应 custom/对话执行结束后才恢复后续 Worker，避免同一 Agent 双路径并行执行相同目标。
- Agent 编辑器开关更名为“不受 Planner 控制”，点击立即通过配置 API 保存，保存中防重入、失败回滚，不再要求额外点击“保存配置”；刷新后状态与服务端一致。普通“保存配置”不再提交该字段，避免页面加载时的旧值把刚选择的 false 覆盖为 true。

## [0.1.5] - 2026-11-07

### 首个意图与 Agent 编排

- 自定义 Agent 新增 `planner_controlled` 配置（默认 `true` 兼容旧数据），Agent 编辑器可设置首个意图执行期间是否受 Planner 控制；`seedFirstIntent` 按创建时配置写入 `planner_policy` 快照，后续修改 Agent 不影响已有 intent，config-export/import 同步支持该字段。
- 首个 `hold_until_done` custom seed 的首次 kick、edge、heartbeat、goalless 和超时终局规划统一由 DB 门控控制：paused/stopped/blocked/exhausted 不再自动唤醒 Planner 或改派默认 Worker，只有真实 done 或人工删除放行；普通 Worker 与其他 custom intent 保持原有失败重规划。
- custom runner 终态改为 fail-closed，只有 `ReasonCompleted` 可写 done，hook/abort 写 stopped，未知或空 reason 写 blocked，避免未完成被误判完成。
- 补齐 Agent 配置 PATCH、DB 状态矩阵、真实 Planner edge/heartbeat、超时收尾、快照兼容和浏览器开关保存测试；实施与验证详见 `dev/FIRST_INTENT_GATE_IMPLEMENTATION.md`、`dev/FIRST_INTENT_GATE_TEST_RECORD.md`。

### 稳定性修复

- 主 Agent 单轮会话增加 10 分钟超时兜底、超时提示和 panic 恢复；正常、超时及 panic 退出均清理会话占用与 LLM 调用计数，手动取消原因保持不变。
- LLM 熔断状态落库改为单 writer + 2000 容量有界队列，队列满时丢弃持久化并记录日志，避免 PostgreSQL 卡顿时按事件堆积 goroutine；新增真实 PostgreSQL 行为测试。
- 冷图压缩和会话执行增加 panic 恢复与堆栈日志，防止单个后台任务异常终止整个服务。

## [0.1.4] - 2026-11-05

### Finding 显式编排

- `spawn_task` 子任务改用独立探索链，不再创建 `source_task_ids` 来源关系；带 `finding_id` 时仅在任务创建事务内复制该 Finding 沿 `yields/derived_from` 反向可达的 `intent/fact/finding` 祖先子图，重映射 origin、节点、边和锚点，复制 intent 固定为 `done`，父 Finding 记录与无关分支保持不变。工具 Schema 和默认 Dispatcher 提示词提供幂等迁移，自定义提示词不覆盖。
- 全部系统工具及 `reporter`、`vuln_dispatcher`、`retester`、`trace_cleaner` 均由代码幂等 seed，首次启动即可使用，不再依赖导入 `config-export`；数据库中的后续修改与删除仍保留。MCP 连接、凭据和可见性继续由用户配置。
- `trace_cleaner` 新增事务化 Agent、默认提示词、4 项工具绑定及显式派发触发器 seed，避免部分初始化；其全局 `on_finding` 默认关闭。

- 新增 Planner 专用 `dispatch_finding_to_agent` 工具：可将当前任务的独立漏洞记录（findings.id）显式派发给任意已启用、非内置的自定义 Agent；具体场景目标和证据门槛由系统提示词约束。
- `vuln_dispatcher` 与 `trace_cleaner` 默认关闭全局 `on_finding` 订阅，改由“外网渗透（内置）”Planner 在确认立足点或仅漏洞路径证据后显式派发；Worker 内置提示词同步强化立足点证据规则。
- 外网 Planner 的 Finding 派发提示词改为可扩展的 Agent 清单和独立规则块，明确 `vuln_dispatcher` 与 `trace_cleaner` 不互斥、清理确认不属于派发前提，避免已取得立足点且存在 `[投放]` 事实时漏派清理审计。
- 新增 `finding_agent_invocations` 持久记录、Conversation 回填和 10 分钟原子补发；合并 Trigger Run 会回填全部 Finding，重复调用返回首次 Conversation ID。
- 新增外网内置 Planner/Worker 提示词、默认任务快照及旧 Dispatcher/Trace Cleaner 触发配置的幂等迁移；自定义模板和自定义任务快照保持不变。
- AI 预审 `note` 改为单条意图的完整中文说明，禁止 ID/分号堆叠；保持 JSON 契约、200 字上限和既有展示方式。
- Worker WebShell 上传要求使用与目标技术栈匹配的正常应用文件名，禁止攻击特征/随机命名及覆盖既有文件，并保持落地路径、`shell_add` 与 `[投放]` 记录一致。
- 外网渗透场景默认关闭意图审核；存量分类一次性迁移，之后手动重开不会被重启覆盖，其他场景默认值不变。

### 修复与发布

- Docker 源码构建恢复 `ARTEX_VERSION` → `main.version` 注入，当前发布版本统一为 **0.1.4**，恢复健康检查版本展示和更新比较能力；运行镜像补齐 `start.sh` 启动必需的 `scripts/artex-config.sh` 与升级 SQL，避免入口脚本启动即退出。
- 拓扑自动整理与 C 段归组增加同步防重入、错误提示与失败重载；归组标记读写容错，手动归组后不再被意外自动归回。
- 人工预审 `pass` / `revise` 的 `note` 统一为必填；空值不进入预审结果，合法理由保留 trim 后内容，自动处置契约不变。
- Agent 触发列表对 Planner 显式派发消息模板显示 `工具调用(dispatch_finding_to_agent)`，不再误显示“无条件”；不额外开启 Scheduler `on_tool_call`，避免重复入队。

## [0.1.3] - 2026-11-04

### 新增

- **内网拓扑图**：任务详情新增资产覆盖图 / 内网拓扑切换视图，配套拓扑画布、编辑工具栏与分组布局，数据表与 API 路由同步落地。

### 变更

- **部署包内置场景预设**：Release 包自带 `config-export/` 编排配置，支持导入场景分类、planner 模板与审核触发器。
- **代码优化**：例行重构与清理，提升可维护性。
- **文档优化**：README 重写为快速开始，部署手册与功能详解同步更新。

## [0.1.2] - 2026-10-30

### 二开功能

- **意图审核流**：新增待审意图、review agent 预审、人工批准 / 驳回、修订重送、乐观锁与可选自动处置；预审建议不会在人工全审模式下直接放行。
- **planner 模板与场景预设**：新增模板库、创建时快照、分类默认模板 / 审核 / 种子意图 / 执行器，以及任务级 worker 并发。
- **自定义执行器**：自定义 agent 可声明 `executor_key`，由种子意图或送审 / 修订时的 `target_executor` 路由；失效时告警并回退 worker。
- **finding 后编排**：通用触发器支持运行策略与水位线；首启播种 `vuln_dispatcher` 和 `reporter`，支持派生场景子任务和自动报告。
- **证据交接**：`report_finding` 支持 `evidence_hint_id`，报告流程按 `evidence_version` 防止旧证据覆盖新报告。
- **可观测性与迁移**：补充关键链路日志、日志级别热切换、访问日志，以及 19 类业务配置的导出 / 幂等导入和提示词升级命令。

### 安全与发布

- 新增首启安装令牌、文件工具任务目录边界与 `python_interpreter` 校验。
- Docker Compose 改为通过 `Dockerfile.source` 从当前二开源码构建，避免误拉上游镜像；JWT key 持久化到数据目录。
- 新增脱敏 `config-export/` 示例包：不含 LLM 档和密钥，MCP 默认禁用；`retester` / `trace_cleaner` 是可选配置，不冒充源码 seed。
- 修正 review 种子提示词与升级 SQL，使“预审只写建议、人工或自动处置才改状态”的文案和代码行为一致。

### 执行模式与任务编排

- 新增 `foothold` / `vulnerability_only` 双执行模式，由后端根据任务目标和描述识别，并注入 Goals、Planner 与漏洞派发上下文。
- 新增 Worker 场景模板、任务级 Worker 提示词快照、场景默认模板和模板校验接口；历史任务保留创建时的执行上下文。
- 新增宿主资产视图、内网任务拓扑、资产派发记录和同资产 Finding 接续能力，减少重复任务链。
- 内网渗透子任务名称由服务端统一生成为 `内网渗透·<目标资产>`，不再依赖模型自觉填写，也不再把长任务摘要作为任务名。

### 提示词与漏洞派发

- 重构 Goals、Planner、Worker、Pentest、Auto、Vuln Dispatcher 和 Trace Cleaner 提示词，统一目标意图、证据验收、投放登记和清理规则。
- GetShell 证据验收区分完整 Shell 与单次命令执行：单次 RCE 回显不再直接等同于取得立足点。
- 漏洞派发仅接受明确的 `foothold` 路径，并要求 `critical/high` 与 WebShell、交互式 Shell 或可用 C2 等实证；仅 RCE、文件上传、信息泄露、未授权接口、弱口令和模糊猜测不创建内网渗透任务。
- 暂停独立漏洞利用和权限提升场景的自动派发；权限提升只作为已创建内网渗透子任务中的内部步骤。
- 派发判断继续服从父任务授权范围。取得 foothold 不会覆盖“禁止内网扫描、横向移动、代理或隧道”等明确限制。

### Review 与前端

- Review 会话恢复原有上下布局，不再把审核操作框错误显示到 Planner、系统审计和普通 Worker 会话。
- 待审队列由多卡片纵向滚动改为 `1、2、3…` 编号切换，一次只显示当前待审意图；保留通过、驳回、修订和 Review 对话。
- 修复会话内容区 flex 高度和静态导出缓存问题，确保部署的前端资源与源码一致。

### MCP 与配置迁移

- `start.sh` 支持 MCP 本地程序、插件和配置目录随包导出；记录文件 SHA256、大小和执行权限。
- 导入时重写 `{{MCP_ROOT}}` 路径、逐文件校验并恢复可执行位；可用 `ARTEX_IMPORT_MCP_BIN=0` 仅导入 MCP 配置。
- 配置包包含 Agent、提示词、触发器、工具绑定、任务分类、Worker 模板和 MCP 配置；同步/Release 配置包不包含 LLM API Key。
- 部署文档统一为源码编译和 Docker 两种方式，并分别提供配置导入命令。

### 构建与源码修复

- 恢复 `evidence/store.go`、`evidence/store_test.go` 和 `evidence/testmain_test.go`，解决源码仍引用 evidence 包但目录缺失导致的构建阻塞。
- 明确前端静态构建使用 `NEXT_EXPORT=1`；清理旧 `.next/out` 后重建，避免旧 Review 分栏资源混入新部署包。
- 修复漏洞派发提示词导入时的 PostgreSQL 角色环境问题，导入失败使用单文件事务回滚，成功后再重启验证。

### 数据库与升级注意事项

- 新增 Worker 模板、资产派发和拓扑相关表及字段；升级前必须备份 PostgreSQL。
- 配置导入不会导入 LLM Key，也不会替代数据库备份；已有任务的历史提示词快照不会被自动改写。
- 二进制升级、配置导入和提示词升级是不同操作，应分别执行并验证。

### 验证

- 子任务命名定向 Go 测试通过；前端 TypeScript 和静态构建通过。
- Chrome 验证主 Agent、Planner、系统审计和 Worker 不显示审核框；Review 编号切换、通过、对话和移动布局通过。
- Linux 二进制部署、健康检查、首页和嵌入前端资源断言通过。
- 完整 `go test ./server` 需要有效的 `ARTEX_PG_DSN`；本次环境未执行完整数据库测试。Docker 部署链路已核读，但未在本次 Windows 环境实机构建镜像。