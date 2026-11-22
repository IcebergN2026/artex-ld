# artex-ld 更新日志

本项目的重要变更记录在此文件中，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

版本说明：`artex-ld` 从 `0.1.2` 起使用独立版本序列。Release 名称为 **artex-ld 0.1.7**，Git Tag 为 **v0.1.7**。文末 `0.3.15` 及更早内容是上游 ARTEX 历史，不属于 `artex-ld` 的版本号序列。

## [Unreleased]

## [0.1.7] - 2026-11-22

### 一键更新发布源迁移至 `IcebergN2026/artex-ld`

- `selfupdate.Repo` 从上游 `Autumn-27/artex` 切换为 `IcebergN2026/artex-ld`，版本检查、Release 元数据与更新包下载统一走二开项目仓库，避免将上游 ARTEX 的独立版本序列误作 artex-ld 更新；前端无需单独配置，仓库信息及 Release 页面链接由后端响应自动派生。
- 保持 GitHub HTTPS 域名白名单、SHA256SUMS 完整性校验和发布包命名约定不变；`go test ./selfupdate` 通过。

### planner 拦截规则标签化、生效链 bug 修复与在线实测（x.x.x.x 已部署 0.1.7）

- 8 条 planner 种子规则名去掉 `[内置] ` 前缀（如 `planner 命令拦截：网络工具`），播种时统一打 `planner拦截` 标签，前端拦截页按标签筛选/分组，与其他规则完全同构。
- 旧库幂等迁移：启动时把 `[内置] planner …` 行改名并补 `planner拦截` 标签，仅动 name/tags，用户改过的正则、启停、优先级、agents 原样保留；迁移后再按新名查重播种，不会产生重复行。
- 前端 `server/webui/dist` 内嵌产物重建：旧产物仍带已删除的 planner 只读专区（不显示正则、不可编辑），新产物即普通规则表（正则可见可改）。
- **worker 作用域修复（实测发现）**：内置 worker 池的真实身份是 `work#N · #intentID`（engine.go 执行名 `work#N`），`agentBaseName` 原先只剥 ` · #N` 后缀，导致 `agents=["worker"]` 的规则对真实 worker 调用永不命中；现把 `work#N`（数字序号）归一为 `worker`，`intercept/agent_scope_test.go` 补 4 例。
- **MCP 规则失效修复（代码审核发现）**：MCP/扩展工具全走 `ExecuteExtraTool` 延迟调用，Guard 此前拿包装器名做匹配且 MCP 工具名不在启用集——`tool_name` 的 `^mcp__…` 规则实际永不生效。现 Guard 在规则评估前解包入参 `tool_name` 得到真实工具名（Match/Judge/Log/HandleAsk 统一用真实名，tool_input 仍给完整包装入参，启用集门控包装器或内部工具任一启用即进）。新增 guard 单测（解包/非包装器不变）+ LiveMatch 的 Guard 全链路 MCP 拦截用例（真实库验证 blocked）。
- **查资料通道**：`plannerBashGuardSpec` 补「纯本地操作被误拦时改用 LS/Glob/Grep/Read，不要派 worker 代替本地读文件」；新增 `plannerWebResearchSpec`——仅当 planner 的 web_search 实际开启（全局开关 ∧ agents.web_search）时追加，指引查公开资料走 web_search/WebFetch、不用 Bash curl/wget、不得指向目标（出口过滤落地前为软约束）。
- **deny 文案幂等升级**：8 条 planner 规则的统一指引追加「查询公开资料请改用 web_search/WebFetch」；存量行仅当消息仍以旧指引结尾（未被用户改过）时替换，自定义结尾不动。
- **搜索后端实测（x.x.x.x）**：公网可达（example/baidu 200），但 DuckDuckGo 被墙（ddgs 超时）、deepseek 后端要求 anthropic 格式 DeepSeek 档（现有 openai 格式 deepseek-flash 档返回 404）。故 planner.web_search 已开、全局总开关暂关；WebFetch 直连公网可用，搜索待 tavily/brave key 或 ddgs 代理。
- **真实链路验证**：新增 `TestPlannerSeedRulesLiveMatch`（`ARTEX_PG_DSN` 驱动，缺行临时补种、结束清理）——8 条规则在 planner 身份下逐条 deny、worker 身份下均不命中；远端 0.1.7 实测「跑一半→暂停→删除→同描述重建」序列中 planner 身份保持 `planner`，2 次越界 Bash 均被 deny 并落 `intercept_pending`。
- **实测暴露的误伤（未改，用户拍板接受）**：`echo "=== i25 nmap"` 的 echo 文本命中网络工具名单；本地目录名 `cmd-output` 的 `cmd` 命中 Shell 包装器名单。两者都是 planner 纯本地读文件被拦。RE2 无环视，彻底区分「命令位置 vs 文本/路径」需改匹配模型（如仅命令位锚定），暂未动。

实施记录：`dev/planner-guard-命令拦截-实施记录.md` §8.12。

### planner 内置拦截规则全部 DB 正则化：代码层判定拆除

- **形态转换**：planner 内置拦截规则不再是代码占有的 Shell AST 结构判定（`guard/planner_bash_policy.go` 已删除），全部改为「系统 → 命令拦截」页的普通 DB 正则规则——启动时幂等播种 8 条 `[内置] planner …` 规则（7 条 `tool_input` 正则 + 1 条 MCP WebShell `tool_name` 正则，`deny`、`priority=1000` 保持内置先行、`agents=["planner"]`），与其他规则同构：可停用/编辑/删除/打标签，消息保留「不要绕过，派发 intent 给 worker」指引。实施记录 `dev/planner-guard-命令拦截-实施记录.md` §8.11。
- **无正则等价物而移除的 2 条**：`parse_error`（Bash 参数解析失败 fail-closed）与 `windows_unsupported`（Windows 整体保守拒绝）——相关调用现落通用拦截链（DB 规则 → LLM 判官）。
- **已知语义弱化**（正则形态固有代价）：匹配对象是工具入参原始 JSON 文本而非解析后的命令 AST，引号切分/变量拼接等混淆可绕过；`http`/`https` 未纳入网络工具正则（会命中所有 URL 字面量）；动态命令只识别命令值以 `$`/反引号开头。
- **拆除**：`guard/planner_bash_policy.go`、`intercept/planner_policy.go`、`server/intercept_planner_rules.go` 整文件删除；7 个 `/api/intercept/planner-rules*` 端点与 `PUT /api/intercept/planner-tags` 删除；5 个 `planner_builtin_*` settings 键启动时幂等清理；`mvdan.cc/sh` 依赖移除；前端拦截页 planner 专区整体移除，选中集回归纯 DB 行。
- **测试**：新增 `server/intercept_planner_seed_test.go`（8 条种子正则可编译 + 21 组命中/误伤边界用例）；`go build ./...`、`go test ./guard/ ./intercept/ ./agent/` 全绿，前端 `tsc --noEmit` 0 诊断。

### 服务指纹识别与「服务明细」表

- 数据链路：`db.PortService` 增加 `product`/`version`（如 nmap -sV 的 nginx/1.24.0），`open_ports` JSONB 持久化携带指纹；IP upsert 与 `AppendIPPort` 的同端口合并偏好带指纹条目（`product` 非空优先），后到扫描的指纹不会被旧的裸端口条目顶掉。`insert_assets` 工具 schema 的 `open_ports` 元素文档化 `product`/`version` 可选键（反序列化自动透传，无迁移）。
- 主机投影：`AssetHostService` 增加 `port`/`technologies`/`page_title`/`status_code`，服务/接口/应用资产归入主机行时带出指纹字段。
- 资产页展示（原型 `asset-redesign-demo.html` 同款）：主机行端口列附首个产品名摘要（悬停显示全部指纹）；行内展开新增「服务明细」整宽表——以端口为基准每端口一行（端口/协议、产品、版本、技术指纹 chips、地址/页面标题/状态码），Web 服务/接口按端口归并，无匹配端口的接口单独成行；KV 区不再堆端口/服务徽章。DSL 检索沿用既有 `technology=` 字段。
- 测试：`TestBuildAssetHostRowsServiceFingerprint`（投影指纹字段）、`TestUpsertIPOpenPortsFingerprintMerge`（合并偏好，PG）；x.x.x.x 实机全部通过，部署后真实数据端到端验证（DB 写入指纹 → 主机投影 → 服务明细表展示）。

### 资产页内外网分区整改与任务管理抽屉

- 页面重构：删除九个资产标签页（主机/内网凭据/企业/根域名/IP/子域名/应用/服务/接口），改为任务过滤条 + DSL 搜索 +「外网资产 / 内网资产」两个主机分区（IP 为基准、各自服务端分页）+ 折叠「其他资产（无 IP 的应用/接口）」；行内展开（KV / 凭据含 secret / 发现）沿用主机视图交互。设计原型 `asset-redesign-demo.html`，实施记录见 `dev/ARTEX-资产页内外网分区整改-实施记录.md`。
- 后端：`AssetHostFilter` 新增 `Intranet *bool` 三态过滤（nil 不过滤）；`GET /api/assets?type=host` 增加可选 `intranet=true|false` 查询参数（`parseBoolTriState`，空/非法值按不过滤）。内外网判定沿用主机投影的 RFC1918 口径（`topologyInternalIPv4`，查询时现算），`insert_assets` 新写入资产无需回填；无 schema 变更，资产写入链路不受影响。
- 任务选择条溢出自动折叠：平铺 Tab 超出容器宽度（ResizeObserver 实测测量副本）时切换为「选中任务 chip + 选择任务 ▾」可搜索下拉（名称/#ID/描述），宽度恢复后自动回到平铺。
- 任务管理抽屉：原「企业管理」入口替换为「任务管理」右侧 Sheet——按状态分组（进行中/已暂停默认展开，已创建/已结束折叠）、任务搜索、暂停/继续（`POST /tasks/:id/control`）、删除（`DELETE /tasks/:id`，可选连带删除名下资产，发现随任务删除避免孤儿）；新建任务跳转任务页。企业管理 API（`createCompany`/`updateCompanyScope`/`deleteCompany`）保留，UI 入口随企业 Tab 一并移除。
- 批量删除：主机行勾选（删除该行全部 `asset_ids`）与「其他资产」勾选合并经 `DELETE /api/assets` 一次提交。
- 前端清理：`intranet-assets.tsx` 与 `api.intranetAssets` 随视图合并删除（后端 `/credentials/intranet/assets` 端点保留）；mock 的 `type=host` 支持 `intranet` 参数、ip 资产返回 auth 投影（含 secret）、同步删除旧内网资产 mock 路由。
- 测试与验收：新增 `TestQueryHostsIntranetFilter`（三态过滤，PG）、`TestParseBoolTriState`（参数解析）；x.x.x.x 实机 `go build ./...`、`go test ./db -run 'TestQueryHostsIntranetFilter|TestQueryHostsAndExactAssetFindingFilter'`、`go test ./server -run TestParseBoolTriState` 全部通过，部署后真实数据浏览器验收（分区计数、行展开 secret、任务过滤、任务管理暂停/删除）通过。

### 拦截规则：标签管理、适用 agent 范围与 planner 内置规则可视化

- `intercept_rules` 新增两列（存量库启动时 `ALTER TABLE … IF NOT EXISTS` 自动升级）：`tags JSONB`（规则分组标签，字符串数组）与 `agents JSONB`（适用 agent 范围，`["all"]` 或具体 agent key 数组，空值归一为 `["all"]`）。
- 规则匹配按 agent 过滤：`Interceptor.Match` 签名改为携带 `context.Context`，仅当规则的 `agents` 含 `all` 或命中 `WithTaskContext` 绑定的 agent 身份时才参与匹配；优先级/首条命中语义不变，空列表向后兼容视为全部。
- 标签管理接口：`GET/PUT /api/intercept/tags`（已知标签存 settings `intercept_tags`，GET 返回已知标签与规则实际使用标签的并集）、`POST /api/intercept/tags/{name}/rename`（重命名：settings 与所有规则引用同步替换）、`DELETE /api/intercept/tags/{name}`（删标签并级联清除所有规则上的引用）。规则 CRUD 请求/响应携带 `tags`、`agents`，服务端做去空白/去重/数量与长度上限校验，`agents` 含 `all` 时收敛为 `["all"]`。
- 规则按 agent 匹配时身份归一为基名：worker lane 绑定身份形如 `worker · #3`（`engine.go` label = agentKey · #intentID），匹配前经 `agentBaseName` 剥离意图序号后缀，自定义 agent lane 同理按 key 命中；`intercept/agent_scope_test.go` 覆盖 `agentBaseName` 与 `ruleAppliesToAgent` 两侧。
- planner 内置规则只读目录：`GET /api/intercept/planner-rules` 返回 `guard.PlannerBuiltinRules()`（10 条代码占有规则的 RuleID/名称/通道/摘要/名单明细），与 Guard 实际判定同源。
- 前端「系统 → 拦截规则」：标签 chips 筛选行（全部 / planner 内置 / 未分类 / 各标签，带规则数）+「标签管理」弹窗（新建/重命名/删除标签，以及**批量设置适用 agent**——行内选择 agent 后一键覆盖该标签下全部规则，走 `POST /api/intercept/tags/{name}/apply-agents`）；planner 内置规则以普通样式的紧凑折叠表置顶展示（先于自定义规则执行、不可关闭，点击行展开名单明细）；规则表名称下内联标签徽标与适用 agent 摘要；编辑器新增「标签」多选（可内联新建）与「适用 agent」选择（全部 / goals / planner / mainagent / worker / 各自定义 agent，打开编辑器时刷新 agent 列表，新增角色立即可选）。「新建规则」保持在最上方操作区。
- 任务详情「拦截」Tab 下方新增 **Guard 审计** 卡片：`GET /api/audit?task=` 逐条展示本任务 Guard 决策（时间/工具/allow|block/RuleID/原因与命令），planner 被代码占有规则拦截的记录在此可见。
- `scripts/artex-config.sh` 导出/导入自定义拦截规则同步携带 `tags`、`agents`（导入缺省 `'[]'`/`'["all"]'`）；已知标签随 settings 整体导出。

### 修复：内置 agent（worker 等）保存运行配置报「agent 不存在或是内置 agent」

- 根因：`SetAgentExecutorKey` 的 UPDATE 带 `AND builtin=false`，而配置面板对内置 agent 固定透传 `executor_key:""`（空串清除），0 行更新被误报为「agent 不存在或是内置 agent」，worker/planner 等内置 agent 点「保存配置」必失败。
- 修复：`db.SetAgentExecutorKey` 先判存在性/内置——内置 agent 空串清除视为空操作成功、非空声明仍按 K18 拒绝；前端 `agent-editor.tsx` 内置 agent 不再携带 `executor_key` 字段。回归测试 `TestAgentConfigBuiltinExecutorKeyNoop`（server/mgmt_test.go）覆盖内置空串保存 200、内置非空 400、自定义设置/清除 200。

### planner Guard 命令结构化拦截

- planner 接入现有 Guard：per-task planner 经 `Planner.SetGuard(t.Guard)` 挂上 `PreToolUse/PostToolUse` Hooks，`Plan()` 内以 `intercept.WithTaskContext(ctx, taskID, "planner", emit)` 绑定 agent 身份（普通规划与任务超时收尾同一身份）；身份只经 ctx 传递、不写 Guard 可变字段，planner 与 worker 共用同一任务 Guard 无串扰。
- planner 专用前置规则（代码占有固定策略，`guard/planner_bash_policy.go`）在通用拦截（DB 规则/LLM 判官/人工审批）之前、Bash 子进程启动之前执行，不依赖 interceptor 是否配置：POSIX 平台用 `mvdan.cc/sh/v3` 解析完整 Shell AST，覆盖 `&&`/`||`/管道/子 Shell/后台命令的所有命令段，识别 curl/wget/nc/nmap/httpx/nuclei/sqlmap 等网络与渗透程序（绝对路径按 basename 归一化）、Shell 包装器（bash/sh/sudo/env/busybox 等）、脚本解释器（python/perl/php/node/awk 等）、间接执行（find -exec/-ok、xargs、tar --checkpoint-action）、命令替换与进程替换、`/dev/tcp`/`/dev/udp` 网络通道；命令位置含变量等动态表达式、解析失败一律 fail-closed。`cat curl_notes.txt`、`grep -R "curl" ./src` 等仅把 curl 当文本参数的命令不误伤，未命中规则的本地命令继续走原 Guard 流程，无 allow 特权。
- planner 不得通过 MCP 执行 WebShell/远程命令：新增工具名正则规则（`planner_mcp_webshell_exec`），godzilla/antsword/behinder 等 WebShell 管理型 MCP 服务器对 planner 整服禁用，任意 MCP 服务器上 `webshell`/`shell_exec`/`execute_command` 等语义的工具同样拦截；worker 与其他 agent 不受影响。
- Windows 平台 planner 的 Bash(PowerShell) 暂无结构化安全分析，按 `planner_bash_windows_unsupported` 保守拒绝（fail-closed），本地目录与文件仍可用 LS/Glob/Grep/Read；worker 的 Windows 行为不变。
- 所有拒绝复用「artex 平台管控·非目标防御」文案并附固定 RuleID（`planner_bash_network_tool` 等），指引模型派发 intent 给 worker 而非改写绕过；每次 block 只产生一条 Guard 审计记录（含原命令），不进入 LLM 判官或人工 ask。
- 提示词同步：默认模板与五套领域模板删除旧「（可选，仅开局、极轻量）探测理解」段（允许 planner 开局 curl 探测的条款），决策流程重编号为 1/2/3/4；静态审核下发说明（`add_review_intent`）只在内网渗透模板保留，其余模板恢复普通 `add_intent` 提交行（任务实际开启审核时的动态最高优先级指令不受影响）。新增代码占有的 `plannerBashGuardSpec` 边界说明，对全部四级模板、DB 当前提示词、用户自定义模板和旧任务快照一律追加。
- 存量提示词一次性迁移 `planner_guard_prompt_v1`：单事务处理内置 `planner_templates` 六行与当前全局 planner prompt（双锚删除探测段并重编号、非内网行完整匹配替换历史审核提交后缀、缺锚不切割、幂等可重跑），不触碰自定义模板、历史版本与任务快照。
- 边界说明：本期约束 planner 的 Bash 与 MCP WebShell 通道，不是操作系统级网络隔离；planner 的 WebFetch、WebSearch、其他 MCP、Skill 与文件工具保持现状。

### 资产页任务维度过滤与主机 Tab

- 「宿主」Tab 更名「主机」；主机、根域名、IP、子域名、应用、服务、接口 Tab 顶部统一新增任务选择条（共享组件 `task-tabs.tsx`，「全部任务」为默认保持原行为）：点击任务后仅显示该任务收集的资产，行保持以 IP 为基准的投影（主域名、解析到该 IP 的子域名、端口、应用/服务/接口、凭据、发现数）。企业 Tab 为全局管理视图不加过滤。后端复用既有 `task_id` 过滤（`AssetHostFilter.TaskID` / `CountByTask` / `QueryDSL`），前端 `api.hostAssets`/`api.assets`/`api.searchAssets` 增加 `taskID` 参数，mock 路由同步支持。
- 主机 Tab 详情改为内网凭据式行内展开（替换原右侧 Sheet 抽屉）：行首 chevron、表格下方单展开面板——左侧域名/端口/服务 KV，右侧凭据分组（发现/认证通过，经 `api.searchAssets("ip=…")` 取 `AggregateAuth` 投影，含 secret 明文），底部该主机发现列表。
- 修复内网资产查询重复行：`QueryIntranetAssetsByTask` 谓词增加 `a.type='ip'` 限制——真实数据模型下同一 IP 的 service/endpoint 资产行会作为空 C 段/空端口的重复行混入内网视图（部署后用真实 service 资产实测发现）。

### 内网资产展开视图（任务维度）

- 「内网凭据」Tab 重构为任务维度资产展开视图：顶部横向滚动任务 Tab（展示全部未删除任务），选择任务后仅加载该任务（含直接来源任务）范围内全部 RFC1918 内网资产（含无凭据资产），IP 行可展开为统一 KV 区（hostname/os/open_ports 等 extra 与端口）+ 单列凭据分组（发现/认证通过）；凭据卡片含类型、账号@域、适用服务、来源、证据、时间与 **Secret 明文**。列固定为 IP / C 段（直读 `assets.c_segment`）/ 端口服务 / 凭据，域名相关字段不占 UI 空间。计划与验收记录见 `dev/ARTEX-内网资产展开视图-详细计划书.md`、`dev/ARTEX-内网资产展开视图-实施记录.md`。
- 资产 API 可见性变化：`AggregateAuth` 凭据投影新增 `secret` 字段，新端点 `GET /api/credentials/intranet/assets?task_id=`（缺省 400）返回任务范围内网资产及凭据（含 secret）；旧平铺接口 `GET /api/credentials/intranet`、旧 DB 查询 `QueryIntranet`、前端 `IntranetCredential` 类型与 `api.intranetCredentials` 全部移除。Agent 侧 `list_assets` / `list_untested_assets` 输出统一经 `stripAuthSecrets` 剥离 secret（`record_credential` / `mark_credential_verified` 本就不含 secret，不变）。
- `insert_assets` 六类资产（root_domain/subdomain/ip/app/http_service/other_service）item schema 与写入链路支持 `extra` 键值信息（主机名、OS、备注等），服务端批量导入同步透传；ON CONFLICT 统一 `extra = assets.extra || EXCLUDED.extra` 合并，同一资产多次写入的键共存。工具 schema 一次性刷新标记 `tool_schema_refresh_v16_asset_extra`。
- 内网渗透 Planner 模板「据凭据 fact」整行替换为凭据表闭环要求：新发现凭据一律 `record_credential`、认证成功一律 `mark_credential_verified`、横向前 `list_assets` 查看 `auth`、`verified` 直接复用、仅 `discovered` 先验证再横向；存量内置模板经一次性迁移 `planner_intranet_credential_loop_v1` 升级，仅迁移仍匹配旧行的记录、不覆盖用户自定义文本，任务提示词快照不动。

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