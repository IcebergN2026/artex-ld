# Artex 架构树与逐文件职责

更新时间：2026-11-02  
路径基准：项目根目录。

本文使用目录树说明架构和文件职责。覆盖业务 Go 源码及测试、`web/src`、数据库 schema、构建/部署配置和 Skills；依赖、构建产物、缓存和 `third_party/` 内部源码不逐文件展开。

## 1. 运行时架构树

```text
Artex
├── Web 控制台：Next.js / React
│   ├── 页面与业务组件
│   ├── API 客户端 / SSE / JWT
│   └── 探索图、拓扑图、任务与系统管理
│
└── Go 服务：默认监听 :8787
    ├── HTTP API 与认证
    ├── Manager：任务和基础设施生命周期
    ├── Engine：planner + 多 worker 调度
    ├── ReviewRunner：任务审核与写操作门
    ├── Scheduler：Agent triggers
    ├── Notifier：通知投递
    ├── Agent Runtime：prompt、模型、工具和轨迹
    │   ├── 内置工具 / 自定义工具
    │   ├── Skill / MCP
    │   ├── Guard / Interceptor
    │   └── LLM Pool / Retry / Recorder
    ├── PostgreSQL：业务数据和持久队列
    └── 本地数据目录：流量、证据、附件、工作区、归档和更新文件
```

## 2. 核心任务链树

```text
创建任务
├── 写入 task / exploration / root / relations
├── 并发准入与运行态恢复
├── 生成 goals
├── Planner 循环
│   ├── 读取探索图和资产态势
│   ├── 创建或修订 intents
│   └── 判断目标是否完成
├── Worker 执行 open intents
│   ├── 调用 LLM / 工具 / Skill / MCP
│   ├── 写入 Activity / facts / findings
│   └── 更新 intent 状态
├── 图变化后再次规划
└── 完成 / 暂停 / 超时 / 人工介入
```

## 3. 项目目录树与每个文件的职责

```text
artex-ld/
├── agent/  # Agent 角色、提示、工具和模型运行时。
│   ├── assembly.go  # 组装 Agent 工具链：合并内置、数据库、Skill 与 MCP 工具，处理 deferred unlock，并把工具 panic 转换为可观测错误。
│   ├── blackboard_inheritance_test.go  # 验证 `blackboard_inheritance` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── cancelcause.go  # 定义带业务代码、短文本和详细说明的取消原因，并通过 context 在调度器与执行器之间传递。
│   ├── capture.go  # 驱动一次 agent session，收集模型消息、工具调用、token 用量和终止原因，转换为数据库 Activity。
│   ├── capture_approval_test.go  # 验证 `capture_approval` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── capture_usage_test.go  # 验证 `capture_usage` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── chat.go  # 实现通用聊天 Agent：加载会话历史、配置模型/工具/代理/搜索并执行多轮对话。
│   ├── coldgraph.go  # 计算探索图的冷热集合、结构冷节点和可折叠分块，为压缩提供图算法。
│   ├── coldgraph_test.go  # 验证 `coldgraph` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── compaction.go  # 执行探索上下文 minor/major compaction，生成摘要节点、维护版本与冷标记并控制并发。
│   ├── customexec.go  # 运行独立自定义执行器会话，配置工具、预算和输出写回，返回终止原因及写入统计。
│   ├── deferred.go  # 把 deferred 工具清单加入系统提示，并从历史工具调用恢复已解锁 Skill 状态。
│   ├── deferred_test.go  # 验证 `deferred` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── execution_mode.go  # 从任务目标识别漏洞专项执行模式，生成 goal、planner 和 worker 使用的模式提示。
│   ├── execution_mode_test.go  # 验证 `execution_mode` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_recorder.go  # 抽象漏洞记录器，将同一 recorder 注入 ToolSet、Worker、Planner 和 MainAgent。
│   ├── finding_recorder_test.go  # 验证 `finding_recorder` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_workflow.go  # 统一漏洞工具的 finding_id、节点 ID 和 traffic_refs 工作流，处理 hint 与流量参数提示。
│   ├── finding_workflow_test.go  # 验证 `finding_workflow` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── foothold_prompt_test.go  # 验证 `foothold_prompt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── goals.go  # 调用模型把用户任务描述拆解为结构化最终目标，写入 goal 节点和初始审计事件。
│   ├── insert_assets_test.go  # 验证 `insert_assets` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── internal_planner_prompt_test.go  # 验证 `internal_planner_prompt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── mainagent.go  # 实现任务主 Agent：面向操作员对话，读取任务态势并调用受限的任务操作工具。
│   ├── noa.go  # 按配置启用 noa 辅助能力、归档目录和会话警告回调。
│   ├── planner.go  # 实现规划者循环：读取态势、生成或修订意图、判定目标，并在图变化后再次运行。
│   ├── planner_domain_templates.go  # 定义内网、提权、利用、漏洞挖掘和代码审计等规划者内置场景提示模板。
│   ├── planner_domain_templates_test.go  # 验证 `planner_domain_templates` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── prompt.go  # 集中渲染 planner、worker、main 和 goals 的提示模板，支持数据库覆盖及变量注入。
│   ├── prompt_now_test.go  # 验证 `prompt_now` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── prompt_test.go  # 验证 `prompt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── promptcatalog.go  # 定义 auto、独立渗透、审核、派发、报告等内置 Agent 的系统提示种子。
│   ├── provider.go  # 从环境或资料创建模型 provider，处理代理、限额识别、故障转移、重试和请求记录。
│   ├── provider_capture_e2e_test.go  # 验证 `provider_capture_e2e` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── provider_capture_test.go  # 验证 `provider_capture` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── provider_quota_test.go  # 验证 `provider_quota` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── provider_responses_test.go  # 验证 `provider_responses` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── proxyenv_test.go  # 验证 `proxyenv` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── retester.go  # 保存漏洞复测 Agent 的默认系统提示。
│   ├── review_context_test.go  # 验证 `review_context` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── runinfo.go  # 在 context 中携带任务、探索、会话和 Agent 等运行元数据，供工具与日志使用。
│   ├── scope_prompt_test.go  # 验证 `scope_prompt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── session_header_test.go  # 验证 `session_header` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── side_questions.go  # 将旁路问题捕获能力附加到一次 agent 运行上下文。
│   ├── side_questions_test.go  # 验证 `side_questions` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── taskclock.go  # 在 context 中传递任务时钟，并把单次运行预算钳制到任务截止时间。
│   ├── terminalreason.go  # 汇总运行轨迹和终止状态，生成面向用户的结束文本、原因标签和进度后缀。
│   ├── terminalreason_test.go  # 验证 `terminalreason` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── testmain_test.go  # 验证 `testmain` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── toolcatalog.go  # 定义内置工具种子、Agent 工具集合、描述/schema 覆盖和默认参数注入。
│   ├── toolcatalog_test.go  # 验证 `toolcatalog` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tools.go  # 领域工具核心实现：图节点、事实、目标、漏洞、任务提示、输出读取及工具调用审计。
│   ├── tools_digest.go  # 实现冷摘要读取、成员解析和 expand_digest 工具，支持按需展开压缩上下文。
│   ├── tools_insert.go  # 实现资产、公司、任务范围写入与查询工具，并按 Agent 类型导出工具集合。
│   ├── tools_intent.go  # 实现意图更新和取消工具，校验 ID/version 并限制主 Agent 对开放意图的操作。
│   ├── tools_intent_test.go  # 验证 `tools_intent` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tools_nil_store_test.go  # 验证 `tools_nil_store` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tools_overview_test.go  # 验证 `tools_overview` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tools_review.go  # 实现审核工具集，以及审核预筛、审核对话和自动审核的独立模型运行流程。
│   ├── unrestricted_prompt_test.go  # 验证 `unrestricted_prompt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── worker.go  # 实现意图执行者：加载意图与约束、执行工具、写回事实/漏洞并处理预算和收尾。
│   ├── worker_delivery_test.go  # 验证 `worker_delivery` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── worker_domain_templates.go  # 定义 Worker 场景模板的类型和内置种子列表。
│   ├── worker_intervention_test.go  # 验证 `worker_intervention` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── worker_prompt_test.go  # 验证 `worker_prompt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   └── wrapup.go  # 管理普通预算耗尽和任务超时的收尾提示、轮数、工具禁用与 settlement 配置。
├── cmd/  # 可执行程序入口。
│   └── artex/
│       ├── main.go  # 进程入口：解析启动参数，初始化日志、更新、信号、Manager 和 HTTP Server。
│       └── main_test.go  # 验证 `main` 对应实现的单元、边界或集成行为；不参与生产运行。
├── config/  # 进程配置。
│   ├── config.go  # 管理数据目录、配置文件路径、Skill 目录和 PostgreSQL DSN，并提供脱敏显示。
│   └── config_test.go  # 验证 `config` 对应实现的单元、边界或集成行为；不参与生产运行。
├── db/  # PostgreSQL 领域模型、持久化、schema 和测试。
│   ├── activity_page_test.go  # 验证 `activity_page` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── asset_dsl.go  # 解析资产筛选 DSL，完成词法/语法分析、字段校验和 SQL 条件生成。
│   ├── asset_intercept.go  # 提供资产拦截规则的增删改查和启停。
│   ├── asset_intercept_match.go  # 将资产域名/IP/URL 与允许、阻断规则匹配，形成资产准入决定。
│   ├── asset_intercept_match_test.go  # 验证 `asset_intercept_match` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── assets.go  # 资产主存储：层级资产 upsert、归一化、查询、公司关联、范围和生命周期操作。
│   ├── assets_test.go  # 验证 `assets` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── chat_mentions.go  # 为聊天 @ 提及提供按类型搜索和游标分页。
│   ├── commands.go  # 查询工具命令与统计，并维护 LLM 调用记录和模型/会话用量查询。
│   ├── companies.go  # 公司主体及范围规则存储，处理范围变更、删除约束和关联资产。
│   ├── companies_test.go  # 验证 `companies` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── company_lock_test.go  # 验证 `company_lock` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── company_scope.go  # 解析并规范化域名、IP、CIDR、ICP 和关键字等公司/任务范围输入。
│   ├── company_scope_consistency_test.go  # 验证 `company_scope_consistency` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── company_scope_test.go  # 验证 `company_scope` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── config.go  # 系统配置主存储：LLM profiles、Agent 配置、工具、模板、变量、可见性和执行器参数。
│   ├── config_max_tokens_test.go  # 验证 `config_max_tokens` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── config_test.go  # 验证 `config` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── constants.go  # 定义探索节点类型、状态、来源等跨存储层共享常量。
│   ├── constraints.go  # 管理任务约束的列表、新增、修改和删除。
│   ├── conversation.go  # 管理普通会话、置顶/重命名/模型绑定、会话 Activity 和 token 摘要。
│   ├── conversation_pin_test.go  # 验证 `conversation_pin` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── customtool_test.go  # 验证 `customtool` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── db.go  # PostgreSQL 连接与初始化入口：建库、连接池、schema/迁移锁、内置 Agent/模板 seed。
│   ├── db_test.go  # 验证 `db` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── digest.go  # 持久化探索压缩轮次、冷标记、内容版本、摘要及摘要成员关系。
│   ├── dispatch_targets.go  # 记录漏洞派发子任务与规范化资产键，检测跨任务重复派发冲突。
│   ├── dispatch_targets_test.go  # 验证 `dispatch_targets` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── exploration.go  # 探索图核心存储：节点、边、意图状态机、Activity、审计、token 与任务生命周期操作。
│   ├── exploration_sources.go  # 在直接来源任务的探索数据上做继承标记、统一查询、分页和 Activity 搜索。
│   ├── exploration_sources_test.go  # 验证 `exploration_sources` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── exploration_test.go  # 验证 `exploration` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── exploration_tokens_test.go  # 验证 `exploration_tokens` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_assets.go  # 将漏洞命中聚合到公司/域名/IP/服务资产树，补充父节点和覆盖统计。
│   ├── finding_assets_test.go  # 验证 `finding_assets` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_invocations.go  # 持久化 Agent×Finding 显式调用，提供 Conversation 回填和原子补发认领。
│   ├── finding_invocations_test.go  # 验证调用幂等、回填、时间门控和原子认领行为。
│   ├── finding_retests.go  # 管理漏洞复测记录、关联会话、状态恢复和复测结果。
│   ├── finding_retests_test.go  # 验证 `finding_retests` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_sanitization_test.go  # 验证 `finding_sanitization` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_traffic.go  # 保存不可变流量证据快照，并在事务中绑定漏洞、维护版本和校验一致性。
│   ├── finding_traffic_archive.go  # 把流量证据纳入任务归档，并在恢复时重建漏洞流量绑定。
│   ├── finding_traffic_archive_test.go  # 验证 `finding_traffic_archive` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── findings.go  # 漏洞主存储：登记、筛选、分组、导出、详情更新、状态与后续意图。
│   ├── findings_test.go  # 验证 `findings` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── host_assets.go  # 将资产转换为按主机聚合的列表行，合并端口服务和漏洞命中统计。
│   ├── host_assets_test.go  # 验证 `host_assets` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intent_admission.go  # 丢弃仍处开放状态的意图，供任务准入和清理流程使用。
│   ├── intent_control_test.go  # 验证 `intent_control` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intent_delete_test.go  # 验证 `intent_delete` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intent_edit.go  # 基于 expectedVersion 原子修改或软删除意图，并写入审计信息。
│   ├── intercept.go  # 管理工具拦截规则、待审批记录、审批状态和历史查询。
│   ├── intercept_detail.go  # 查询拦截详情、上下文与审计，并处理审批决定及最终工具执行结果。
│   ├── intercept_detail_test.go  # 验证 `intercept_detail` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intercept_execution.go  # 根据审批 ID 唯一定位原始 run/toolUse/任务会话，供批准后重放工具。
│   ├── intercept_execution_test.go  # 验证 `intercept_execution` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intercept_filter_test.go  # 验证 `intercept_filter` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intercept_seed_test.go  # 验证 `intercept_seed` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── jsonb_clean_test.go  # 验证 `jsonb_clean` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── llm_records_migrate_test.go  # 验证 `llm_records_migrate` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── llm_usage.go  # 持久化模型 token 用量，按任务、profile、日期和 judge 维度聚合。
│   ├── llmhealth.go  # 保存、读取和清除 LLM profile 健康状态。
│   ├── llmretry.go  # 定义全局/profile LLM 重试规则，执行默认值合并、范围钳制和持久化。
│   ├── llmretry_test.go  # 验证 `llmretry` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── logs.go  # 将运行日志写入数据库，并支持最近查询、游标分页和过期清理。
│   ├── migrate_topology.go  # 执行拓扑颜色、描述、节点类型和系统边标签等兼容迁移。
│   ├── nkey.go  # 为域名、IP、端口、服务、站点、端点、参数和技术生成规范化自然键。
│   ├── notification.go  # 管理通知渠道和事件，按渠道过滤/限流并展开为投递记录。
│   ├── notification_delivery.go  # 实现通知投递队列：租约认领、摘要批次、重试、退避、完成和失败。
│   ├── notification_test.go  # 验证 `notification` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── planner_builtin_edit_test.go  # 验证 `planner_builtin_edit` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── review_flow_test.go  # 验证 `review_flow` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── scenario_template_migration.go  # 将既有外网渗透默认模板迁移到当前内置模板版本。
│   ├── schema.sql  # PostgreSQL 数据库结构：表、约束、索引、内置数据和兼容 DDL。
│   ├── settings.go  # 提供通用字符串和布尔 settings 的读取、写入及默认值处理。
│   ├── settings_insert_test.go  # 验证 `settings_insert` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── side_questions.go  # 持久化旁路问题快照、请求幂等、历史、回复、重放和记忆。
│   ├── side_questions_test.go  # 验证 `side_questions` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── skill_usage.go  # 记录 Skill 调用，按 Skill 或任务统计成功、失败和最近调用。
│   ├── skill_usage_test.go  # 验证 `skill_usage` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_archive_aggregate_stats.go  # 将已归档任务的 Agent/Skill 聚合统计合并回在线统计视图。
│   ├── task_archive_state_test.go  # 验证 `task_archive_state` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_archives.go  # 管理任务归档快照、归档/恢复/删除队列、任务领取和归档列表。
│   ├── task_archives_restore.go  # 在事务中恢复归档任务、探索、资产、关系和 LLM 数据，并重映射外键。
│   ├── task_archives_test.go  # 验证 `task_archives` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_assets.go  # 管理任务与资产关联、来源说明、任务范围注册和意图资产清单。
│   ├── task_assets_context.go  # 合并直接任务及来源任务的范围、覆盖和未测试资产上下文。
│   ├── task_assets_test.go  # 验证 `task_assets` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_categories.go  # 管理任务分类及默认审核、模板、资产模式、首意图等预设。
│   ├── task_categories_test.go  # 验证 `task_categories` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_category_nkey_test.go  # 验证 `task_category_nkey` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_context.go  # 批量补充任务来源、公司、LLM profile 路由和上下文信息。
│   ├── task_context_lock_test.go  # 验证 `task_context_lock` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_context_unit_test.go  # 验证 `task_context_unit` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_delete_concurrency_test.go  # 验证 `task_delete_concurrency` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_intercept.go  # 管理任务级资产准入规则，支持列表、创建、修改、启停和删除。
│   ├── task_list_performance_test.go  # 验证 `task_list_performance` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_metadata_test.go  # 验证 `task_metadata` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_queue_test.go  # 验证 `task_queue` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_scope.go  # 管理任务范围、自动/Agent 范围来源、测试覆盖统计和覆盖图。
│   ├── task_scope_test.go  # 验证 `task_scope` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_source_limit_test.go  # 验证 `task_source_limit` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_templates.go  # 提供任务模板 CRUD、名称规范化、规则序列化和创建任务时的模板应用。
│   ├── task_templates_test.go  # 验证 `task_templates` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_topology.go  # 记录节点间拓扑关系，规范化输入并返回更新后的拓扑图。
│   ├── task_topology_editor.go  # 拓扑编辑器存储与算法：节点/边/分组、环路检测、孤立修复、布局和持久化。
│   ├── task_topology_editor_test.go  # 验证 `task_topology_editor` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_topology_test.go  # 验证 `task_topology` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tasks.go  # 任务聚合根存储：创建、来源/公司/LLM 绑定、状态流转、查询、删除准备和恢复。
│   ├── tasks_test.go  # 验证 `tasks` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── testmain_test.go  # 验证 `testmain` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tool_usage.go  # 写入工具调用记录并按工具汇总调用次数。
│   ├── tool_usage_test.go  # 验证 `tool_usage` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tools.go  # 管理内置和自定义工具定义、Agent 绑定、启停及 schema/desc 更新。
│   ├── topology_fact_edge.go  # 从 fact payload 解析拓扑实体并自动派生拓扑边。
│   ├── topology_fact_edge_test.go  # 验证 `topology_fact_edge` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── triggers.go  # 管理 Agent 触发器、调度游标和 finding/goal/timeout/task/tool 事件查询。
│   ├── worker_output_test.go  # 验证 `worker_output` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── worker_templates.go  # 提供 Worker 提示模板 CRUD、内置保护、默认模板切换和原子 PATCH。
│   └── worker_templates_test.go  # 验证 `worker_templates` 对应实现的单元、边界或集成行为；不参与生产运行。
├── enrich/  # 资产异步丰富。
│   └── enrich.go  # 后台异步执行 DNS 解析和 HTTP 站点探测，将标题、IP 等结果写回资产。
├── evidence/  # 漏洞证据正文存储。
│   ├── store.go  # 管理请求/响应正文的 hash 寻址存储、证据快照、绑定、导出、恢复和 GC。
│   ├── store_test.go  # 验证 `store` 对应实现的单元、边界或集成行为；不参与生产运行。
│   └── testmain_test.go  # 验证 `testmain` 对应实现的单元、边界或集成行为；不参与生产运行。
├── guard/  # 工具调用审计 hooks。
│   ├── guard.go  # 注册工具调用前后 hooks，执行审计/拦截，并识别常见 WAF/阻断输出。
│   ├── guard_intercept_test.go  # 验证 `guard_intercept` 对应实现的单元、边界或集成行为；不参与生产运行。
│   └── guard_test.go  # 验证 `guard` 对应实现的单元、边界或集成行为；不参与生产运行。
├── intercept/  # 工具拦截、审批和 judge。
│   ├── intercept.go  # 工具拦截核心：规则编译、风险判断、待审批队列、审核写操作门和执行控制。
│   ├── prompt.go  # 定义 judge 默认提示、输入边界、输出协议和裁决解析。
│   ├── prompt_test.go  # 验证 `prompt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── review_context.go  # 构造 judge 审查输入，携带工作目录、用户背景和工具参数。
│   ├── review_context_test.go  # 验证 `review_context` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── trace.go  # 跟踪工具调用上下文、完成结果和审计链，控制记录长度与内容摘要。
│   └── trace_test.go  # 验证 `trace` 对应实现的单元、边界或集成行为；不参与生产运行。
├── llmpool/  # LLM 路由与健康池。
│   ├── health.go  # 维护 LLM profile 健康状态、软/硬熔断、退避冷却和持久化恢复。
│   ├── health_policy_test.go  # 验证 `health_policy` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── pool.go  # 对多个 LLM profile 排序、轮询和故障转移，统一 Complete/Stream 调用。
│   └── pool_test.go  # 验证 `pool` 对应实现的单元、边界或集成行为；不参与生产运行。
├── llmrec/  # LLM 调用记录。
│   ├── capture.go  # 捕获原始 LLM 请求及多次响应尝试，通过 Tee 读取响应体。
│   ├── capture_test.go  # 验证 `capture` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── llmrec.go  # 包装 LLM provider，关联任务/Worker 上下文并落库请求、响应、时延和 token。
│   └── llmrec_test.go  # 验证 `llmrec` 对应实现的单元、边界或集成行为；不参与生产运行。
├── mcphttp/  # MCP HTTP/SSE 客户端。
│   ├── client.go  # 实现 Streamable HTTP 与旧 SSE MCP 客户端、初始化、工具发现和远程调用。
│   └── client_sse_test.go  # 验证 `client_sse` 对应实现的单元、边界或集成行为；不参与生产运行。
├── notify/  # 通知渠道与渲染。
│   ├── channel.go  # 定义通知渠道接口、注册表、配置读取和永久错误语义。
│   ├── channels_test.go  # 验证 `channels` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── dingtalk.go  # 实现钉钉 webhook 校验、签名和消息发送。
│   ├── email.go  # 实现 SMTP/TLS 邮件配置、连接、MIME 构造和发送。
│   ├── email_test.go  # 验证 `email` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── event.go  # 定义通知快照、条目和消息模型，以及状态变化标题。
│   ├── feishu.go  # 实现飞书签名、交互卡片、消息分批和 webhook 发送。
│   ├── filter.go  # 解析并匹配通知的严重度、任务、事件和关键字过滤条件。
│   ├── filter_test.go  # 验证 `filter` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── html.go  # 将通知消息渲染为 HTML，处理严重度颜色、摘要和转义。
│   ├── http.go  # 提供通知 HTTP 客户端、内网目标防护、JSON 请求及敏感 URL/错误脱敏。
│   ├── http_test.go  # 验证 `http` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── markdown.go  # 将通知渲染为 Markdown，处理摘要、批量条目和字节预算。
│   ├── mask.go  # 对通知凭据做掩码显示和安全合并，防止更新时覆盖或泄露 secret。
│   ├── mask_test.go  # 验证 `mask` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── notify.go  # 定义通知类型、严重度和状态的常量、标签与比较。
│   ├── pack_test.go  # 验证 `pack` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── redact_test.go  # 验证 `redact` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── render.go  # 提供通知文本截断、资产摘要和按大小计算批量条目数。
│   ├── render_test.go  # 验证 `render` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── sign_test.go  # 验证 `sign` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── ssrf_test.go  # 验证 `ssrf` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── telegram.go  # 实现 Telegram Bot API 配置、HTML 消息和长度分批。
│   ├── webhook.go  # 实现通用 webhook 配置、Go template 消息体和发送。
│   ├── webhook_template_test.go  # 验证 `webhook_template` 对应实现的单元、边界或集成行为；不参与生产运行。
│   └── wecom.go  # 实现企业微信 webhook Markdown 消息和长度限制。
├── report/  # 任务和漏洞报告。
│   ├── findings.go  # 生成漏洞清单/单漏洞 Markdown、CSV、文件名和证据引用内容。
│   └── report.go  # 将任务目标、发现、统计和节点内容渲染为任务 Markdown 报告。
├── scripts/  # 项目维护与迁移脚本。
│   ├── cdp-shot.js  # 通过 Chrome DevTools Protocol 截取页面，用于 UI 调试/验证。
│   ├── test-deploy-scripts.sh  # 部署脚本的静态/行为回归检查。
│   └── upgrade-review-flow-prompts.sql  # 将既有 review-flow 提示升级到新版流程的 SQL 迁移。
├── selfupdate/  # 自更新与回滚。
│   ├── bootstrap.go  # 启动时应用已 staged 更新，执行 smoke test、确认或回滚。
│   ├── github.go  # 查询 GitHub 最新 Release，校验目标 host 并选择平台资产。
│   ├── selfupdate.go  # 定义更新路径、marker、版本比较、Docker 检测和重启退出码。
│   ├── selfupdate_test.go  # 验证 `selfupdate` 对应实现的单元、边界或集成行为；不参与生产运行。
│   └── stage.go  # 下载并校验 SHA256SUMS 和二进制 ZIP，解压、检查可写性并报告进度。
├── server/  # HTTP API、应用服务和执行调度。
│   ├── accesslog.go  # HTTP access log 中间件，生成请求 ID、记录状态并支持流式 Flush。
│   ├── accesslog_test.go  # 验证 `accesslog` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── assembly.go  # 连接 Agent 扩展点与数据库，seed prompt/tool 并注册领域工具。
│   ├── assembly_test.go  # 验证 `assembly` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── asset_intercept.go  # 提供全局资产拦截规则 HTTP CRUD 和请求校验。
│   ├── assets.go  # 提供公司、范围、资产、归属调整、统计和批量资产操作 API。
│   ├── assets_scope_test.go  # 验证 `assets_scope` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── auth.go  # 实现密码初始化/登录/修改、JWT 密钥、签名验证和认证中间件。
│   ├── auth_test.go  # 验证 `auth` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── broadcast.go  # 实现按任务订阅的 Activity 内存广播器，支撑实时事件推送。
│   ├── chat_llm_resolve_test.go  # 验证 `chat_llm_resolve` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── chat_mentions.go  # 解析聊天 @ 实体引用，搜索实体并将有界上下文注入消息。
│   ├── chat_mentions_test.go  # 验证 `chat_mentions` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── chatupload.go  # 处理聊天附件上传、安全文件名和附件上下文消息组合。
│   ├── commands.go  # 提供工具命令、LLM 原始记录、模型用量和统计查询/删除 API。
│   ├── constraints_api.go  # 提供任务约束 CRUD，以及 planner/worker 注入开关。
│   ├── conversation_status_test.go  # 验证 `conversation_status` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── conversations.go  # 提供通用会话 CRUD、消息/附件、流式发送、停止、重试和模型配置 API。
│   ├── core_test.go  # 验证 `core` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── customtool.go  # 管理 command/script/HTTP 自定义工具，提供测试、校验和 CoreTool 适配。
│   ├── customtool_test.go  # 验证 `customtool` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── dispatch_assets.go  # 解析漏洞派发资产键、回填派发目标并检测内部资产重复任务。
│   ├── dispatch_assets_test.go  # 验证 `dispatch_assets` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── dto.go  # 定义 API DTO，并将数据库任务、节点、漏洞、流量等模型转换为 JSON 结构。
│   ├── engine.go  # 任务执行内核：planner/worker 调度、运行控制、重试、steer、恢复、取消和删除屏障。
│   ├── engine_cancelcause_test.go  # 验证 `engine_cancelcause` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── engine_emptyturn_test.go  # 验证 `engine_emptyturn` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── engine_llm_calls_test.go  # 验证 `engine_llm_calls` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── engine_timeout.go  # 实现任务 deadline、settling、最终 planner 轮次和在途操作协调。
│   ├── execution_mode_test.go  # 验证 `execution_mode` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── external_review_migration.go  # 一次性关闭外网渗透旧默认审核，保留后续手动重开。
│   ├── external_review_migration_test.go  # 验证外网默认关闭、其他分类不变及手动重开保护。
│   ├── finding_handoff.go  # 实现漏洞交接工具，将规范化漏洞上下文追加到任务/Agent。
│   ├── finding_handoff_test.go  # 验证 `finding_handoff` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_invocations.go  # 实现通用 Finding 显式派发工具、消息构造与统一入队。
│   ├── finding_invocations_test.go  # 验证派发校验、幂等、消息、回填和 Scheduler 补发行为。
│   ├── finding_migrations_test.go  # 验证系统提示词、默认绑定和旧 Finding 入口迁移。
│   ├── finding_orchestration_migration.go  # 迁移 Dispatcher 提示词并关闭两个旧全局 Finding 入口。
│   ├── finding_prompt_migration.go  # 升级外网内置 Planner/Worker 提示词和默认任务快照。
│   ├── finding_retests.go  # 提供漏洞复测 API、复测 Agent 工具、启动和结束处理。
│   ├── finding_retests_test.go  # 验证 `finding_retests` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_traffic.go  # 提供漏洞流量证据查询、绑定、编辑、预览、正文下载和 Agent 工具。
│   ├── finding_traffic_export.go  # 将漏洞报告及请求/响应证据写入 ZIP 导出结构。
│   ├── finding_traffic_test.go  # 验证 `finding_traffic` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── finding_workflow.go  # 注册漏洞工作流工具并校验 Agent 对证据的读/写权限。
│   ├── finding_workflow_test.go  # 验证 `finding_workflow` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── findings_groups.go  # 提供漏洞资产树、分组列表和深挖后续任务 API。
│   ├── findings_groups_test.go  # 验证 `findings_groups` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── goals.go  # 负责目标生成、任务启动、并发准入、排队、恢复和运行协调。
│   ├── goals_api.go  # 提供任务 goal 的列表、新增、编辑和删除 API。
│   ├── goals_test.go  # 验证 `goals` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── inheritance_api_test.go  # 验证 `inheritance_api` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── inheritance_dto_test.go  # 验证 `inheritance_dto` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intent_intervention.go  # 提供向运行中 Worker 发送 steer 消息的 API 和请求校验。
│   ├── intercept.go  # 提供工具拦截规则/审批/历史 API，并接入 LLM judge reviewer。
│   ├── intercept_detail_test.go  # 验证 `intercept_detail` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intercept_filter_test.go  # 验证 `intercept_filter` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intercept_live_test.go  # 验证 `intercept_live` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── intercept_review_test.go  # 验证 `intercept_review` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── llmpool.go  # 将数据库 LLM profile 转为 pool member，构建路由链并导出健康状态。
│   ├── llmpool_test.go  # 验证 `llmpool` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── llmrec_raw_test.go  # 验证 `llmrec_raw` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── llmretry.go  # 把全局/profile 重试策略应用到 Agent 和 Engine。
│   ├── llmretry_test.go  # 验证 `llmretry` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── logsink.go  # 捕获标准日志到内存、数据库和订阅者，提供级别解析与游标。
│   ├── manager.go  # 应用生命周期中心：数据库、任务运行态、LLM 状态、代理、流量、enrich 和任务删除。
│   ├── manager_delete_files_test.go  # 验证 `manager_delete_files` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── manager_lifecycle_test.go  # 验证 `manager_lifecycle` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── mcpdiscover.go  # 连接 MCP server、发现工具、缓存定义并在启动时补发现。
│   ├── mgmt_test.go  # 验证 `mgmt` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── notifier.go  # 后台通知投递器：实时/摘要、限流、渲染、重试、退避和渠道适配。
│   ├── notify_api.go  # 提供通知渠道和投递记录 CRUD、测试、重试及 DTO 转换。
│   ├── notify_api_test.go  # 验证 `notify_api` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── orchestration.go  # 实现主 Agent 的平台编排工具：任务、Agent、LLM、MCP、触发器和派发操作。
│   ├── orchestration_name_test.go  # 验证 `orchestration_name` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── planner_templates.go  # 提供 Planner 模板 CRUD、默认切换和变量校验 API。
│   ├── platform_tools.go  # 实现资产删除、Skill/自定义工具/MCP 管理等平台工具。
│   ├── platform_tools_test.go  # 验证 `platform_tools` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── prompt_vars_test.go  # 验证 `prompt_vars` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── review.go  # 审核运行器：任务预筛、审核会话、自定义执行器、写操作门和运行槽位。
│   ├── review_chat_e2e_test.go  # 验证 `review_chat_e2e` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── review_http_test.go  # 验证 `review_http` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── scenario_templates_test.go  # 验证 `scenario_templates` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── scheduler.go  # 后台触发器调度器：处理六类事件，并每 10 分钟补发未创建 Conversation 的 Finding 调用。
│   ├── scope_prompt_migration.go  # 对旧 Agent 提示执行范围授权语义的一次性迁移。
│   ├── scope_prompt_migration_test.go  # 验证 `scope_prompt_migration` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── server.go  # HTTP 服务与运行配置中心：路由、provider 缓存、Agent 配置、聊天、流式接口和后台组件启动。
│   ├── server_mgmt.go  # 大型管理 API 集合：任务、Agent、Skill、LLM、设置、日志、工具和通用请求辅助。
│   ├── side_questions.go  # 接入旁路问题服务，管理模型、快照、运行、取消、回复和 SSE 事件。
│   ├── side_questions_test.go  # 验证 `side_questions` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── skill_upload_test.go  # 验证 `skill_upload` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── skill_usage.go  # 包装 Skill 工具调用并记录成功、失败和参数等使用台账。
│   ├── skill_zip.go  # 校验 Skill ZIP 的压缩方法、路径和条目安全性。
│   ├── sync_scopesentry.go  # 通过 MCP 同步 ScopeSentry 数据源、项目、任务和资产。
│   ├── task_admission_test.go  # 验证 `task_admission` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_archive_package.go  # 将任务、图、资产、漏洞、流量和统计打包为可恢复归档。
│   ├── task_archive_package_test.go  # 验证 `task_archive_package` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_archives.go  # 提供任务归档、恢复、删除、列表和后台归档处理 API。
│   ├── task_archives_test.go  # 验证 `task_archives` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_assets.go  # 提供任务资产关联、范围、覆盖和未测试资产 API。
│   ├── task_categories.go  # 提供任务分类 CRUD 和默认预设配置 API。
│   ├── task_categories_test.go  # 验证 `task_categories` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_control.go  # 提供任务暂停、继续、停止、重试、运行控制和状态操作 API。
│   ├── task_control_routes_test.go  # 验证 `task_control_routes` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_delete_barrier_test.go  # 验证 `task_delete_barrier` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_intercept.go  # 提供任务级资产准入规则 API。
│   ├── task_llm.go  # 提供任务 LLM 绑定、切换、profile 路由和配额状态 API。
│   ├── task_llm_test.go  # 验证 `task_llm` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_metadata.go  # 提供任务名称、分类、描述、来源和公司等元数据 API。
│   ├── task_metadata_test.go  # 验证 `task_metadata` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_resolution.go  # 解析请求中的任务 ID、运行态和关联数据库对象。
│   ├── task_templates.go  # 提供任务模板 CRUD 和按模板创建任务的 API。
│   ├── task_templates_test.go  # 验证 `task_templates` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── task_topology_editor.go  # 提供拓扑图、节点、边、分组、布局和编辑器操作 API。
│   ├── task_topology_scope_test.go  # 验证 `task_topology_scope` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── testmain_test.go  # 验证 `testmain` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tool_usage.go  # 提供工具调用统计和使用明细 API。
│   ├── tool_usage_test.go  # 验证 `tool_usage` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── tools_wire_test.go  # 验证 `tools_wire` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── trigger_merge_test.go  # 验证 `trigger_merge` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── triggers.go  # 提供 Agent trigger CRUD、启停和事件配置 API。
│   ├── update.go  # 提供版本查询、更新下载、安装和回滚 API。
│   ├── update_test.go  # 验证 `update` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── vuln_dispatcher_foothold_test.go  # 验证 `vuln_dispatcher_foothold` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── vuln_dispatcher_nkey_test.go  # 验证 `vuln_dispatcher_nkey` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── webui_embed.go  # 在 embedui 构建中嵌入并托管 `web/out` 静态控制台。
│   ├── webui_stub.go  # 非 embedui 构建的空 Web UI 实现，避免引入静态资源。
│   ├── worker_delivery_migration.go  # 迁移旧 Worker 交付/完成语义和内置提示。
│   ├── worker_delivery_migration_test.go  # 验证 `worker_delivery_migration` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── worker_message_test.go  # 验证 `worker_message` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── worker_templates.go  # 提供 Worker 模板 CRUD、默认切换和变量校验 API。
│   ├── worker_templates_test.go  # 验证 `worker_templates` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── worker_topology_fact_migration.go  # 迁移 Worker 从事实派生拓扑边的内置行为。
│   ├── worker_topology_migration.go  # 迁移 Worker 拓扑记录提示和内置模板。
│   ├── worker_topology_migration_test.go  # 验证 `worker_topology_migration` 对应实现的单元、边界或集成行为；不参与生产运行。
│   └── workspace.go  # 管理任务/会话工作目录、路径安全和工作区文件 API。
├── sidequestion/  # 旁路问题服务。
│   ├── capture.go  # 从主会话轨迹捕获可旁路回答的问题和上下文。
│   ├── context.go  # 定义旁路问题的父子上下文和上下文键。
│   ├── context_test.go  # 验证 `context` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── request.go  # 定义旁路问题请求、回复、幂等和输入结构。
│   ├── service.go  # 协调旁路问题运行、历史、回复、重放和记忆。
│   └── sidequestion_test.go  # 验证 `sidequestion` 对应实现的单元、边界或集成行为；不参与生产运行。
├── skills/  # 运行时 Skills 与参考文档。
│   ├── api-recon/
│   │   ├── scripts/
│   │   │   ├── build_perm_tree.py  # 从前端认证/路由模块重建宽松权限与菜单 stub。
│   │   │   ├── extract_route_map.py  # 从 JS chunk 提取 routeMap/routeLink，生成 route_map.json。
│   │   │   ├── harvest_static.py  # 下载静态页面/JS、解析 manifest 与端点，形成站点 recon 输出。
│   │   │   ├── package-lock.json  # API recon Node 依赖锁定文件。
│   │   │   ├── package.json  # API recon 浏览器脚本依赖和运行命令。
│   │   │   ├── preload.js  # 在页面早期注入运行时 hook，捕获路由、请求和前端行为。
│   │   │   ├── runtime_harvest.js  # 浏览器运行时信息采集脚本，补充静态下载无法获得的数据。
│   │   │   └── spider_mpa.py  # 同域 MPA 爬取模板，按深度/数量/cookie 策略收集页面和链接。
│   │   ├── reference.md  # API recon 的方法、参数、输出和站点适配参考。
│   │   └── SKILL.md  # API  recon Skill 的触发条件、操作流程和使用约束。
│   ├── playwright-cli/
│   │   ├── references/
│   │   │   ├── element-attributes.md  # Playwright 元素定位与属性操作参考。
│   │   │   ├── playwright-tests.md  # Playwright 测试编写与运行参考。
│   │   │   ├── request-mocking.md  # 请求拦截和 mock 参考。
│   │   │   ├── running-code.md  # 在浏览器会话执行代码的参考。
│   │   │   ├── session-management.md  # 浏览器会话创建、复用和关闭参考。
│   │   │   ├── storage-state.md  # 登录态/cookie/storage state 保存与恢复参考。
│   │   │   ├── test-generation.md  # 自动生成 Playwright 测试参考。
│   │   │   ├── tracing.md  # Playwright trace 采集与分析参考。
│   │   │   └── video-recording.md  # 浏览器视频录制参考。
│   │   └── SKILL.md  # Playwright CLI Skill 的浏览器自动化操作流程。
│   └── scopesentry/
│       └── SKILL.md  # ScopeSentry 数据查询/同步 Skill 的使用说明。
├── third_party/
│   └── norma/  # 本地 Agent/LLM/tool harness 第三方源码（未逐文件展开）。
├── traffic/  # 流量捕获、证据与归档。
│   ├── archive.go  # 将本地流量数据纳入归档和恢复流程。
│   ├── archive_test.go  # 验证 `archive` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── evidence.go  # 从流量记录生成漏洞证据引用、快照和预览。
│   ├── passthrough_test.go  # 验证 `passthrough` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── proxy_test.go  # 验证 `proxy` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── reclaim_test.go  # 验证 `reclaim` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── traffic.go  # 本地 MITM/HTTP 流量捕获与存储核心，管理 exchange、请求/响应和查询。
│   ├── traffic_store_test.go  # 验证 `traffic_store` 对应实现的单元、边界或集成行为；不参与生产运行。
│   ├── traffic_test.go  # 验证 `traffic` 对应实现的单元、边界或集成行为；不参与生产运行。
│   └── upgrade_test.go  # 验证 `upgrade` 对应实现的单元、边界或集成行为；不参与生产运行。
├── web/  # Next.js 控制台。
│   ├── src/  # 前端业务源码。
│   │   ├── app/  # Next App Router 页面与布局。
│   │   │   ├── (auth)/
│   │   │   │   ├── login/
│   │   │   │   │   └── page.tsx  # 登录页，提交凭据、保存认证状态并进入主控制台。
│   │   │   │   ├── setup/
│   │   │   │   │   └── page.tsx  # 首次管理员初始化页，创建密码并进入系统。
│   │   │   │   └── layout.tsx  # 认证页面布局，提供登录/初始化页共用的居中容器和视觉结构。
│   │   │   ├── (external)/
│   │   │   │   └── page.tsx  # 外部入口/重定向页，根据运行模式引导到控制台或认证流程。
│   │   │   ├── (main)/
│   │   │   │   ├── _components/
│   │   │   │   │   ├── sidebar/
│   │   │   │   │   │   ├── account-switcher.tsx  # 账户显示/切换控件，提供账号相关入口。
│   │   │   │   │   │   ├── app-sidebar.tsx  # 控制台侧栏骨架，组合导航、账户、主题、布局和搜索功能。
│   │   │   │   │   │   ├── change-password-dialog.tsx  # 修改当前管理员密码的对话框。
│   │   │   │   │   │   ├── layout-controls.tsx  # 切换侧栏、内容宽度和布局偏好的控件。
│   │   │   │   │   │   ├── nav-main.tsx  # 渲染主导航分组、当前路由状态和折叠行为。
│   │   │   │   │   │   ├── nav-user.tsx  # 用户菜单，集成修改密码、退出等账户操作。
│   │   │   │   │   │   ├── search-dialog.tsx  # 全局导航搜索对话框，按关键字跳转功能页面。
│   │   │   │   │   │   └── theme-switcher.tsx  # 切换明暗主题和主题预设。
│   │   │   │   │   ├── main-content.tsx  # 主内容容器，处理侧栏布局、间距和页面内容出口。
│   │   │   │   │   └── update-badge.tsx  # 显示新版本状态并提供更新入口。
│   │   │   │   ├── chat/
│   │   │   │   │   └── page.tsx  # 通用聊天工作台：会话列表、消息、附件、@ 引用、模型和流式响应。
│   │   │   │   ├── dashboard/
│   │   │   │   │   └── page.tsx  # 仪表盘，聚合任务、漏洞、资产、token、活动和系统运行指标。
│   │   │   │   ├── function/
│   │   │   │   │   ├── assets/
│   │   │   │   │   │   ├── _components/
│   │   │   │   │   │   │   └── host-assets.tsx  # 按主机展示资产、端口服务与漏洞统计。
│   │   │   │   │   │   └── page.tsx  # 公司/资产管理页，提供范围、筛选、层级和资产操作。
│   │   │   │   │   ├── commands/
│   │   │   │   │   │   └── page.tsx  # 工具命令审计页，查询调用参数、结果、任务和执行统计。
│   │   │   │   │   ├── findings/
│   │   │   │   │   │   ├── _components/
│   │   │   │   │   │   │   ├── asset-tree.tsx  # 漏洞资产树，按公司、域名、IP 和服务浏览命中。
│   │   │   │   │   │   │   └── findings-table.tsx  # 漏洞表格，提供筛选、排序、状态和详情操作。
│   │   │   │   │   │   ├── detail/
│   │   │   │   │   │   │   ├── lineage.tsx  # 展示漏洞来源、后续意图、复测和派发等血缘关系。
│   │   │   │   │   │   │   └── page.tsx  # 漏洞详情工作台，整合证据、状态、复测、血缘和后续操作。
│   │   │   │   │   │   └── page.tsx  # 漏洞总览页，组合资产树、分组和漏洞列表。
│   │   │   │   │   ├── llm-records/
│   │   │   │   │   │   └── page.tsx  # LLM 调用记录页，查看请求/响应、时延、模型和 token。
│   │   │   │   │   ├── sync/
│   │   │   │   │   │   └── page.tsx  # ScopeSentry 同步页，选择数据源/项目并导入资产。
│   │   │   │   │   ├── tasks/
│   │   │   │   │   │   ├── detail/
│   │   │   │   │   │   │   ├── _tabs/
│   │   │   │   │   │   │   │   ├── topology/
│   │   │   │   │   │   │   │   │   ├── asset-refs-section.tsx  # 拓扑节点资产引用区，关联节点与资产记录。
│   │   │   │   │   │   │   │   │   ├── export-formats.ts  # 定义拓扑导入/导出格式及序列化辅助。
│   │   │   │   │   │   │   │   │   ├── graph-model.ts  # 定义拓扑图模型、节点/边类型和图数据转换。
│   │   │   │   │   │   │   │   │   ├── topology-canvas.tsx  # 基于 React Flow 的拓扑画布、节点边渲染和交互。
│   │   │   │   │   │   │   │   │   ├── topology-edge-dialog.tsx  # 新建/编辑拓扑边的对话框。
│   │   │   │   │   │   │   │   │   ├── topology-editor.tsx  # 拓扑编辑状态容器，协调画布、工具栏、drawer 和持久化。
│   │   │   │   │   │   │   │   │   ├── topology-import-dialog.tsx  # 导入拓扑文件、校验格式并预览变更。
│   │   │   │   │   │   │   │   │   ├── topology-node-drawer.tsx  # 查看/编辑拓扑节点属性、资产引用和描述。
│   │   │   │   │   │   │   │   │   └── topology-toolbar.tsx  # 拓扑编辑工具栏，提供新增、布局、导入和导出操作。
│   │   │   │   │   │   │   │   ├── assets-tab.tsx  # 任务资产页签，管理范围、资产关联和资产清单。
│   │   │   │   │   │   │   │   ├── broadcast-tab.tsx  # 任务动态页签，展示 Activity 流、Agent 消息和运行事件。
│   │   │   │   │   │   │   │   ├── coverage-graph-tab.tsx  # 任务覆盖图页签，展示已测/未测资产关系。
│   │   │   │   │   │   │   │   ├── findings-tab.tsx  # 任务漏洞页签，筛选并操作当前任务的漏洞。
│   │   │   │   │   │   │   │   ├── graph-tab.tsx  # 探索图页签，可视化 fact/intent/goal/finding 等节点与边。
│   │   │   │   │   │   │   │   ├── intercept-tab.tsx  # 任务拦截页签，查看工具审批、历史和执行上下文。
│   │   │   │   │   │   │   │   ├── overview-tab.tsx  # 任务概览页签，展示目标、状态、约束、统计和控制入口。
│   │   │   │   │   │   │   │   ├── report-tab.tsx  # 任务报告页签，查看和导出 Markdown 报告。
│   │   │   │   │   │   │   │   ├── retests-tab.tsx  # 漏洞复测页签，启动复测并查看结果和关联会话。
│   │   │   │   │   │   │   │   ├── review-panel.tsx  # 任务审核面板，展示审核对话、结论和审核操作。
│   │   │   │   │   │   │   │   ├── sessions-tab.tsx  # 任务会话页签，按 Agent/Worker 查看消息轨迹和 token。
│   │   │   │   │   │   │   │   └── topology-tab.tsx  # 任务拓扑页签，加载图数据并挂载编辑器。
│   │   │   │   │   │   │   └── page.tsx  # 任务详情总容器，加载任务、组织页签、实时事件和任务操作。
│   │   │   │   │   │   └── page.tsx  # 任务列表/创建页，管理分类、模板、状态和任务入口。
│   │   │   │   │   ├── traffic/
│   │   │   │   │   │   └── page.tsx  # HTTP 流量查看页，筛选 exchange 并检查请求/响应。
│   │   │   │   │   └── workspace/
│   │   │   │   │       └── page.tsx  # 工作区文件页，浏览、查看和管理任务/会话文件。
│   │   │   │   ├── system/
│   │   │   │   │   ├── agents/
│   │   │   │   │   │   ├── detail/
│   │   │   │   │   │   │   └── page.tsx  # 单个 Agent 配置页，编辑提示、工具、模型、模板和触发器。
│   │   │   │   │   │   └── page.tsx  # Agent 列表页，创建、启停和进入 Agent 配置。
│   │   │   │   │   ├── intercept/
│   │   │   │   │   │   ├── approvals/
│   │   │   │   │   │   │   └── page.tsx  # 工具审批中心，处理待审批调用并查看执行结果。
│   │   │   │   │   │   ├── assets/
│   │   │   │   │   │   │   └── page.tsx  # 全局资产准入规则页，配置 allow/block 匹配。
│   │   │   │   │   │   └── page.tsx  # 工具拦截规则页，配置动作、模式、judge 和启停。
│   │   │   │   │   ├── llm/
│   │   │   │   │   │   ├── _components/
│   │   │   │   │   │   │   └── retry.tsx  # LLM 重试策略编辑组件。
│   │   │   │   │   │   └── page.tsx  # LLM profile 管理页，配置模型、凭据、路由、重试和健康状态。
│   │   │   │   │   ├── logs/
│   │   │   │   │   │   └── page.tsx  # 系统日志页，实时/分页查看日志并按级别筛选。
│   │   │   │   │   ├── mcp/
│   │   │   │   │   │   └── page.tsx  # MCP server 管理页，配置连接、发现工具和启停。
│   │   │   │   │   ├── notify/
│   │   │   │   │   │   ├── _components/
│   │   │   │   │   │   │   ├── channel-fields.ts  # 定义不同通知渠道的表单字段、secret 和目标配置。
│   │   │   │   │   │   │   ├── channel-form.tsx  # 通知渠道新增/编辑表单。
│   │   │   │   │   │   │   ├── delivery-list.tsx  # 通知投递记录列表，展示状态并提供重试。
│   │   │   │   │   │   │   └── stat-tile.tsx  # 通知统计小卡片。
│   │   │   │   │   │   └── page.tsx  # 通知中心，管理渠道、过滤器、测试消息和投递记录。
│   │   │   │   │   ├── settings/
│   │   │   │   │   │   ├── _components/
│   │   │   │   │   │   │   └── update-card.tsx  # 系统更新卡片，展示版本、下载进度、安装和回滚。
│   │   │   │   │   │   └── page.tsx  # 系统设置页，管理偏好、数据、运行参数和更新。
│   │   │   │   │   ├── skills/
│   │   │   │   │   │   └── page.tsx  # Skill 管理页，浏览目录、上传 ZIP、编辑文件和启停。
│   │   │   │   │   └── tools/
│   │   │   │   │       └── page.tsx  # 工具管理页，配置内置/自定义工具、Agent 绑定和 schema。
│   │   │   │   └── layout.tsx  # 主控制台布局与客户端认证门，组合侧栏、偏好和页面内容。
│   │   │   ├── favicon.ico  # 浏览器 favicon 静态资源。
│   │   │   ├── globals.css  # 全局 Tailwind/CSS 入口，定义设计变量、基础样式和主题 preset 引用。
│   │   │   ├── icon.png  # 应用图标静态资源。
│   │   │   ├── layout.tsx  # Next 根布局，设置 metadata、字体、主题启动、偏好 Provider 和全局反馈组件。
│   │   │   └── not-found.tsx  # Next 404 页面。
│   │   ├── components/  # 业务组件与基础 UI 组件。
│   │   │   ├── ui/
│   │   │   │   ├── alert-dialog.tsx  # 封装 `alert-dialog` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── alert.tsx  # 封装 `alert` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── avatar.tsx  # 封装 `avatar` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── badge.tsx  # 封装 `badge` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── button.tsx  # 封装 `button` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── card.tsx  # 封装 `card` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── chart.tsx  # 封装 `chart` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── checkbox.tsx  # 封装 `checkbox` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── collapsible.tsx  # 封装 `collapsible` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── combobox.tsx  # 封装 `combobox` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── command.tsx  # 封装 `command` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── dialog.tsx  # 封装 `dialog` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── drawer.tsx  # 封装 `drawer` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── dropdown-menu.tsx  # 封装 `dropdown-menu` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── empty.tsx  # 封装 `empty` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── field.tsx  # 封装 `field` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── hover-card.tsx  # 封装 `hover-card` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── input-group.tsx  # 封装 `input-group` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── input.tsx  # 封装 `input` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── item.tsx  # 封装 `item` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── label.tsx  # 封装 `label` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── native-select.tsx  # 封装 `native-select` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── pagination.tsx  # 封装 `pagination` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── popover.tsx  # 封装 `popover` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── progress.tsx  # 封装 `progress` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── radio-group.tsx  # 封装 `radio-group` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── resizable.tsx  # 封装 `resizable` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── scroll-area.tsx  # 封装 `scroll-area` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── select.tsx  # 封装 `select` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── separator.tsx  # 封装 `separator` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── sheet.tsx  # 封装 `sheet` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── sidebar.tsx  # 封装 `sidebar` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── skeleton.tsx  # 封装 `skeleton` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── sonner.tsx  # 封装 `sonner` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── sortable-head.tsx  # 封装 `sortable-head` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── spinner.tsx  # 封装 `spinner` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── switch.tsx  # 封装 `switch` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── table.tsx  # 封装 `table` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── tabs.tsx  # 封装 `tabs` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── textarea.tsx  # 封装 `textarea` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── toggle-group.tsx  # 封装 `toggle-group` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   ├── toggle.tsx  # 封装 `toggle` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   │   └── tooltip.tsx  # 封装 `tooltip` 基础 UI 组件及统一样式/变体，供业务组件复用。
│   │   │   ├── agent-editor.tsx  # Agent 编辑表单，配置基本属性、prompt、工具和运行参数。
│   │   │   ├── approval-execution-focus.tsx  # 聚焦展示已批准工具的重放执行状态与结果。
│   │   │   ├── approval-records.tsx  # 通用审批记录列表和审批操作组件。
│   │   │   ├── asset-dsl-search.tsx  # 资产 DSL 搜索输入、语法提示和查询触发组件。
│   │   │   ├── asset-intercept-rules-editor.tsx  # 资产 allow/block 规则编辑表格。
│   │   │   ├── copy-button.tsx  # 通用复制到剪贴板按钮及反馈。
│   │   │   ├── exploration-graph.tsx  # 探索图可视化组件，渲染节点、边、状态和详情交互。
│   │   │   ├── finding-retest-dialog.tsx  # 启动漏洞复测的配置对话框。
│   │   │   ├── finding-retest-panel.tsx  # 漏洞复测记录、进度和结果面板。
│   │   │   ├── finding-traffic-panel.tsx  # 漏洞绑定流量列表及证据管理面板。
│   │   │   ├── http-code-block.tsx  # HTTP 请求/响应文本高亮、复制和只读展示。
│   │   │   ├── link-traffic-dialog.tsx  # 为漏洞选择并绑定流量证据的对话框。
│   │   │   ├── markdown.tsx  # 通用 Markdown 渲染器，处理代码、表格、链接和安全策略。
│   │   │   ├── mention-textarea.tsx  # 支持 @ 实体搜索、插入和高亮的聊天输入框。
│   │   │   ├── planner-template-controls.tsx  # Planner 模板选择、默认设置和编辑入口控件。
│   │   │   ├── planner-template-manager.tsx  # Planner 提示模板 CRUD、校验和默认模板管理器。
│   │   │   ├── scope-text-editor.tsx  # 多行范围文本编辑与规范化输入组件。
│   │   │   ├── side-question-workspace.tsx  # 旁路问题工作区，展示问题、回答、历史和重放。
│   │   │   ├── status-badge.tsx  # 将任务/意图/漏洞等状态映射为统一徽标。
│   │   │   ├── table-pagination.tsx  # 通用表格分页、页码和页大小控件。
│   │   │   ├── task-llm-profile-chain.tsx  # 展示和调整任务 LLM profile 链及活动路由。
│   │   │   ├── task-template-controls.tsx  # 任务模板选择和预设应用控件。
│   │   │   ├── todo-popover.tsx  # 展示 Agent/任务待办事项的弹出面板。
│   │   │   ├── traffic-evidence-viewer.tsx  # 请求/响应证据查看器，支持正文、偏移和快照。
│   │   │   ├── traffic-picker-dialog.tsx  # 通用流量记录搜索与选择对话框。
│   │   │   ├── transcript.tsx  # Agent 消息/工具调用轨迹渲染组件。
│   │   │   ├── worker-template-controls.tsx  # Worker 模板选择、默认设置和编辑入口控件。
│   │   │   └── worker-template-manager.tsx  # Worker 提示模板 CRUD、校验和默认模板管理器。
│   │   ├── config/
│   │   │   └── app-config.ts  # 前端应用名称、描述和全局展示配置。
│   │   ├── hooks/  # React hooks。
│   │   │   ├── use-current-user.ts  # 读取并缓存当前认证用户状态。
│   │   │   ├── use-mobile.ts  # 监听媒体查询并返回移动端布局状态。
│   │   │   └── use-side-questions.ts  # 管理旁路问题数据、SSE、回答和取消交互。
│   │   ├── lib/  # API、类型、领域转换和工具函数。
│   │   │   ├── fonts/
│   │   │   │   └── registry.ts  # 注册可用字体及其 CSS 变量/加载信息。
│   │   │   ├── mock/
│   │   │   │   ├── data.ts  # mock 模式的页面演示数据和实体夹具。
│   │   │   │   ├── enabled.ts  # 判断前端 mock 模式是否启用。
│   │   │   │   └── handler.ts  # 在 mock 模式下模拟 API 端点和异步响应。
│   │   │   ├── preferences/
│   │   │   │   ├── layout-utils.ts  # 布局偏好计算和规范化辅助。
│   │   │   │   ├── layout.ts  # 定义布局偏好类型、默认值和操作。
│   │   │   │   ├── preferences-config.ts  # 定义偏好存储键、版本和默认配置。
│   │   │   │   ├── preferences-storage.ts  # 持久化、迁移和恢复用户界面偏好。
│   │   │   │   ├── theme-utils.ts  # 主题 class、变量和系统主题计算辅助。
│   │   │   │   └── theme.ts  # 定义主题模式/preset 类型和状态操作。
│   │   │   ├── activity-merge.test.mjs  # 验证 `activity-merge` 对应前端逻辑的 Node 单元测试。
│   │   │   ├── activity-merge.ts  # 合并实时与历史 Activity，处理排序、去重和更新。
│   │   │   ├── api.ts  # 统一前端 API 客户端、认证、错误、SSE 和各业务端点封装。
│   │   │   ├── auth.ts  # 管理 token、认证状态、登录/退出和鉴权辅助。
│   │   │   ├── chat-mentions.test.mjs  # 验证 `chat-mentions` 对应前端逻辑的 Node 单元测试。
│   │   │   ├── chat-mentions.ts  # 解析和序列化聊天 @ 实体引用。
│   │   │   ├── chat-send-mode.ts  # 定义消息发送模式及 Enter/Ctrl+Enter 行为。
│   │   │   ├── company-scope.ts  # 处理公司范围输入、类型和展示辅助。
│   │   │   ├── cookie.client.ts  # 客户端 cookie 读写和删除辅助。
│   │   │   ├── local-storage.client.ts  # 带 key 规范的 localStorage 安全读写辅助。
│   │   │   ├── side-questions.ts  # 旁路问题前端类型、API 和数据转换。
│   │   │   ├── sort-preference.ts  # 持久化表格排序字段和方向。
│   │   │   ├── status.ts  # 状态到中文标签、颜色和样式的映射。
│   │   │   ├── task-assets.ts  # 任务资产前端类型、键和展示转换。
│   │   │   ├── types.ts  # 前端共享业务 DTO 类型，与 Go API 响应对应。
│   │   │   └── utils.ts  # 通用 className 合并和基础工具函数。
│   │   ├── navigation/  # 导航声明。
│   │   │   └── sidebar/
│   │   │       └── sidebar-items.ts  # 声明侧栏导航树、路由、图标和权限展示信息。
│   │   ├── scripts/
│   │   │   ├── generate-theme-presets.ts  # 生成/校验主题 preset 的维护脚本。
│   │   │   └── theme-boot.tsx  # 首屏前注入主题启动脚本，避免主题闪烁。
│   │   ├── stores/  # 客户端状态 store。
│   │   │   └── preferences/
│   │   │       ├── preferences-provider.tsx  # 偏好 React Provider，初始化 store 并订阅系统变化。
│   │   │       └── preferences-store.ts  # 偏好状态 store，集中处理主题、布局和持久化动作。
│   │   ├── styles/  # 主题与样式。
│   │   │   ├── flag-icons/
│   │   │   │   └── flags.css  # 引入/定义国旗图标样式，供语言或区域显示使用。
│   │   │   └── presets/
│   │   │       ├── brutalist.css  # 定义 `brutalist` 主题预设的颜色、圆角、阴影等 CSS 变量。
│   │   │       ├── soft-pop.css  # 定义 `soft-pop` 主题预设的颜色、圆角、阴影等 CSS 变量。
│   │   │       └── tangerine.css  # 定义 `tangerine` 主题预设的颜色、圆角、阴影等 CSS 变量。
│   │   ├── proxy.disabled.ts  # 保留的禁用版 Next proxy 实现，用于需要时恢复对照。
│   │   └── proxy.ts  # Next 服务端路由代理/认证重定向，处理登录和初始化访问。
│   ├── .gitignore  # 前端依赖、Next 构建和本地文件忽略规则。
│   ├── biome.json  # Biome lint/format 规则、文件范围和忽略配置。
│   ├── components.json  # shadcn/ui 组件生成器路径、样式和别名配置。
│   ├── LICENSE  # 前端分发相关许可证。
│   ├── next-env.d.ts  # Next.js 自动生成的 TypeScript 环境类型引用。
│   ├── next.config.mjs  # 按默认/mock/export 模式配置 rewrite、静态输出、trailingSlash 和构建行为。
│   ├── package-lock.json  # npm 依赖精确锁定文件。
│   ├── package.json  # 前端 scripts、Next/React/Radix 等依赖和工具配置。
│   ├── pnpm-lock.yaml  # pnpm 依赖锁定文件；与 npm lock 对应不同包管理器工作流。
│   ├── postcss.config.mjs  # PostCSS/Tailwind CSS 处理配置。
│   ├── tsconfig.json  # 前端应用 TypeScript 编译选项、路径别名和包含范围。
│   ├── tsconfig.scripts.json  # 前端维护脚本的 TypeScript 编译配置。
│   └── tsconfig.tsbuildinfo  # TypeScript incremental build 缓存，不是手写源码。
├── xlog/  # 日志封装。
│   ├── xlog.go  # 提供带级别、上下文和结构化字段的项目日志封装。
│   └── xlog_test.go  # 验证 `xlog` 对应实现的单元、边界或集成行为；不参与生产运行。
├── .dockerignore  # 限制 Docker build context，排除依赖、产物、数据和临时文件。
├── .env.example  # 提供环境变量配置示例，说明部署时可覆盖的参数。
├── .gitattributes  # 定义 Git 文本、换行和二进制文件处理规则。
├── .gitignore  # 排除依赖、构建产物、运行数据、日志和本地配置。
├── ARCHITECTURE.md  # 本文档：架构树和逐文件职责。
├── build.sh  # 跨平台发布构建入口：编译前端/Go、打包 ZIP 并生成 SHA256SUMS。
├── CHANGELOG.md  # 版本变更记录。
├── dev/CODE_REVIEW.md  # 代码审核、冗余清理和后续风险报告（开发文档，本地保留不同步）。
├── config.example.json  # PostgreSQL DSN 等运行配置示例。
├── deploy.sh  # 部署脚本，准备目录/服务并安装或更新应用。
├── dev.sh  # 本地开发入口，启动前后端开发服务及必要环境。
├── docker-compose.bench.yml  # 基准/测试环境的 Compose 服务编排。
├── docker-compose.yml  # 生产容器编排，定义应用、PostgreSQL、卷、端口和健康检查。
├── Dockerfile  # 发布镜像构建：使用预构建产物组装运行环境。
├── Dockerfile.source  # 从源码构建前端和 Go，再生成运行镜像。
├── docs/  # 项目流程和设计文档（非代码，未逐文件展开）。
├── go.mod  # Go module、Go 版本、直接依赖和本地 third_party replace 声明。
├── go.sum  # Go 依赖内容校验和，保证可重复下载。
├── install.sh  # Linux 安装入口，处理依赖、前后端构建/安装和服务初始化。
├── LICENSE  # 项目许可证。
├── README.md  # 项目介绍、功能说明和快速开始。
├── reset-password.sh  # 管理员密码重置工具，操作运行数据中的认证配置。
├── start.bat  # Windows 启动入口，设置环境并启动 Artex。
├── start.sh  # Linux/macOS 启动入口，设置环境并启动 Artex。
└── update.sh  # 已有安装更新入口，重建/替换程序并保留运行数据。
```
