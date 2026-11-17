# 任务轨迹(Trajectory)Tab — 计划书与落地记录

- 日期:2026-11-14
- 版本:artex-ld 0.1.6(Unreleased)
- 部署验证:`<DEPLOY_HOST_A>` / `/opt/artex`,二进制版本 `0.1.6-trajectory-test`

---

# 第一部分:计划书(实施前)

## Context

用户需要在任务详情页看到一个任务下**所有 agent**(mainagent / planner+goals / 各 worker 意图执行)的完整轨迹:思考(thinking)、工具调用(tool_use/tool_result)、文本输出(text/result),按时间线合并展示,运行中实时滚动,并可按 agent 与步骤类型筛选。

侦察结论(已核实):数据已完整存在,无需新增采集。

- `activity` 表(exploration 作用域)已持久化全部 kind:`thinking`(detail 存全文)、`text`、`tool_use`(detail=入参 JSON)、`tool_result`(detail=输出全文)、`result`(终答+token)、`usage`、`round`、`user`、`intent`、`intercept_request`、`llm_switch`/`llm_failover`。写入:`server/engine.go` `emitActivity` → `(*db.ExplorationStore).AppendActivity`(db/exploration.go:1480);发射器 `agent/capture.go` `captureRunSession`。
- 后端已有 `GET /api/exploration/activity/history?task=&session=&before=&limit=`(server/server.go `activityHistory`),倒序分页、返回升序 items + `snapshot_cursor` + `has_more`;`session` 此前只支持 `""`/`main`/`main:N`/`plan`/`intent:<id>`(`parseActivitySession`)。`db.ActivitySessionFilter` 零值 = 整个任务不过滤(db/exploration.go `cond()` default 分支返回空条件,已读确认)。任务级 SSE `GET /api/exploration/activity/stream?task=&since=` 推送全部 lane 的 activityDTO。单步全文 `GET /api/exploration/activity/{seq}`。
- 前端已有渲染器 `web/src/components/transcript.tsx` `Transcript({activity, live?, taskId?, ...})`:`groupSteps` 渲染 thinking 折叠块、tool_use/tool_result 配对、result、round 分隔线、user/intent 气泡;`usage` kind 自动跳过;传入数组含 >1 种 worker 时自动显示彩色 worker chip。默认 detail 懒加载走任务域 `api.activityDetail`。
- 前端已有合并工具 `web/src/lib/activity-merge.ts` `mergeActivities`(按 seq 去重升序),SSE+历史分页范式见 `_tabs/sessions-tab.tsx`(:1019-1123)。

缺口:① `parseActivitySession` 无"整个任务合并"分支;② 前端无合并时间线视图。

## Approach

### 1. 后端:history 端点支持 `session=all`(server/server.go)

- `parseActivitySession` 新增分支:
  ```go
  case sess == "all":
      // 零值过滤器 = 整个任务全部 lane(见 ActivitySessionFilter.cond default 分支)。
      return db.ActivitySessionFilter{}, true
  ```
  并更新函数 doc 注释,把会话键列表加上 `all`。
- `activityHistory` 其余路径无需改动:`filter.Main==false` 跳过 MainSeg 解析;`activitySessionStore` 因 `NodeID==nil` 返回 `t.Store`(不含继承来源任务的 trace —— 继承钻取仍走会话 tab,本视图只展示本任务)。`snapshot_cursor`/`has_more` 语义不变。
- 不改 `s.activity`/`streamActivity`/`activityDetail`,不改 db 层,无迁移。

### 2. 后端测试(server/activity_history_all_test.go,新文件)

- 参照 `server/inheritance_api_test.go` / `server/review_http_test.go` 的测试装配(`NewManager` + 建任务 + `task.Store.AppendActivity` + `signJWT` + `s.Handler()` HTTP 断言;无 PG 时 skip)。
- 用例:建一个任务,`AppendActivity` 写入 worker 分别为 `mainagent`、`planner`、`work#1`(带 `NodeID`)的各 1 条;`GET /api/exploration/activity/history?task=<id>&session=all` → 200,items 升序且三个 worker 齐全;`session=all&before=<中间 seq>` 只返回更旧的页且 `has_more`/`earliest_cursor` 正确;`session=bogus`/`main:-1`/`intent:xyz` 仍 400(回归);`main`/`plan`/`intent:<id>` 仍 200(回归)。

### 3. 前端 API(lib/api/tasks.ts)

- 不改封装:轨迹页直接调现有 `api.activityHistory(taskId, "all", before, limit)`(tasks.ts:692,session 原样透传)。

### 4. 新组件 web/src/app/(main)/function/tasks/detail/_tabs/trajectory-tab.tsx

新建 `export function TrajectoryTab({ taskId }: { taskId: string })`,模式照搬 sessions-tab 的 boot 序列但只有单流:

- **状态**:`items: Activity[]`、`loading`、`error`、`sseLive`;分页游标走 ref(`earliestRef`/`hasMoreRef`/`loadingMoreRef`);`workersFilter: Set<string>`(空集=全选语义)、`kindsFilter: Set<KindGroup>`。
- **启动 effect**(`taskId` 变化重建,带 `alive`/`reqToken` 防 supersede):
  1. `api.activityHistory(taskId, "all", 0, 200)` → `mergeActivities([], r.items)`(返回已升序)、记录 `r.snapshotCursor`。
  2. `new EventSource(sseUrl(`/api/exploration/activity/stream?task=${encodeURIComponent(taskId)}&since=${snapshotCursor}`))`;`onopen/onerror` 置 `sseLive`(EventSource 自动重连);`onmessage` `JSON.parse` 为 `Activity`(失败忽略该帧),`setItems(prev => mergeActivities(prev, [a]))`;用户在底部(`atBottomRef`,viewport scroll 事件维护,距底 < 40px 视为在底)时滚到底。
  3. 清理:`es.close()`、`alive=false`。
- **向上翻历史**:ScrollArea viewport `scroll` 到顶(`scrollTop <= 80`)且 `hasMore && !loadingMore` 时 `api.activityHistory(taskId, "all", earliestSeq, 200)`,prepend 合并(`mergeActivities(older, prev)`);记录加载前 scrollHeight/scrollTop,加载后 `requestAnimationFrame` 恢复相对滚动位置(避免跳动)。在途响应用 `taskIdRef` + boot token 三重防护(then/catch/finally),防旧任务响应污染新任务时间线。
- **筛选 UI**(顶部一行,本地过滤,不打请求):
  - agent chips:从 `items` 收集去重 worker 值,固定排序 `mainagent`→`planner`→`review`→`work#1..N`→其他;label 映射 `mainagent→主 Agent`、`planner→规划`、`review→审核`、`work#N→Worker N`,其余原样;chip 点击切换(空集→仅它;单选再点→恢复全选;多选toggle),tooltip 随状态变化。同时显示该 worker 的步数。
  - 类型开关(默认全开):`思考`(kind=thinking)、`文本`(text)、`工具`(tool_use、tool_result)、`结果`(result)、`系统`(round、user、intent、intercept_request、llm_switch、llm_failover 及其余未列 kind)。`usage` 恒过滤(Transcript 也跳过)。
  - `visible = items.filter(a => kindPass(a) && (workersFilter.size===0 || workersFilter.has(a.worker)))`。
- **渲染**:`<ScrollArea>` 内 `<Transcript activity={visible} live={sseLive} taskId={taskId} />`;不传 `chat`/`fetchDetail`(默认任务域 detail 懒加载)。多 worker 时 Transcript 自动出 worker chip。钉底:effect 依赖 `items/筛选`(变化时钉底则保持),`ResizeObserver` 观察内容高度兜底懒加载 detail 撑高。空数据显示"暂无轨迹记录";`error` 用 `toast.error` 并显示重试按钮(重跑 boot)。
- 文件头加与其他 _tabs 一致的中文 docstring 注释块(文件用途 + 主要函数/类型)。

### 5. 注册 tab(web/src/app/(main)/function/tasks/detail/page.tsx)

- `import { TrajectoryTab } from "./_tabs/trajectory-tab";`
- `TABS` 在 `{ value: "sessions", label: "会话" }` 之后插入 `{ value: "trajectory", label: "轨迹" }`。
- 在 `sessions` 的 `TabsContent` 之后加 `<TabsContent value="trajectory" className="mt-0"><TrajectoryTab key={id} taskId={id} /></TabsContent>`(`key={id}` 按任务重挂载,与 `RetestsTab` 同模式,切换任务不残留旧筛选/旧滚动)。

## Critical files & anchors

- `server/server.go` `parseActivitySession` — 唯一后端改动点,加 `all` 分支。
- `server/server.go` `activityHistory` — 确认零值 filter 直通 `ActivityPage`(只读参考)。
- `web/src/app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx:1019-1123` — SSE+历史分页 boot 序列的复制范本;**避免**其按 session 分发的 store 结构(本功能单流)。
- `web/src/components/transcript.tsx` — `groupSteps` kind 分派与 `Transcript` props;复用为主,落地时修了一处配对健壮性(见第二部分)。
- `web/src/app/(main)/function/tasks/detail/page.tsx` — TABS 与 TabsContent 注册点。

## Verification(计划)

1. 后端单测:`go test ./server -run TestActivityHistoryAll -v`,再 `go build ./...`。
2. 前端编译:`cd web && npm run build`。
3. 端到端冒烟:登录后打开 `/function/tasks/detail?id=<任务>` → 「轨迹」tab;验证合并时间线/worker chip/思考展开/工具配对/到顶翻页/运行中实时追加;筛选交互。
4. 回归:`go test ./server`(session=main/plan/intent 既有行为不变,`session=bogus` 仍 400)。

## Assumptions & contingencies

- 入口=任务详情页新 tab、SSE 实时+历史分页、内容=activity 流(不含 llm_records 原始报文)、筛选=agent+类型且保留时间线 —— 用户已确认。
- archived 任务:轨迹 tab 与会话 tab 走同一 `ResolveTask`/store 路径,不做特殊处理;归档任务 404 与会话 tab 行为一致即视为符合预期。

---

# 第二部分:落地记录(实施后)

## 变更清单

| 文件 | 变更 |
|---|---|
| `server/server.go` | `parseActivitySession` 新增 `case sess == "all"`(零值过滤器)+ doc 注释;`createTask` 无界 `json.NewDecoder` 改为 `decodeJSON(w, r, &req, maxCreateTaskBodyBytes)` |
| `server/jsonbody.go` | const 块新增 `maxCreateTaskBodyBytes = 1 << 20` |
| `server/activity_history_all_test.go` | 新。`TestActivityHistoryAll`:三 lane 合并/分页/bogus 400/main·plan·intent 回归 |
| `web/src/components/transcript.tsx` | `groupSteps` 工具配对键由 `tool_use_id` 改为 `worker + "\n" + tool_use_id`(合并多 agent 时间线时跨 worker 重复 id 不再错配);`AnswerBlock`/`UserRow` 新增 `showWorker` 出 worker chip(合并视图结果/意图有归属标识) |
| `web/src/app/(main)/function/tasks/detail/_tabs/trajectory-tab.tsx` | 新。`TrajectoryTab` 单流合并时间线(SSE + 翻页 + 筛选),见第一部分 Approach 4 |
| `web/src/app/(main)/function/tasks/detail/page.tsx` | 注册「轨迹」tab,`key={id}` 重挂载 |

## 顺带修复:HEAD 既有损坏(阻塞验证)

`server/jsonbody_test.go`(HEAD 已提交)引用从未定义的 `maxCreateTaskBodyBytes` 且断言 createTask 超限 413,导致整个 server 测试包无法编译。按 `jsonbody.go` 头注释"不允许再出现无界 json.NewDecoder(r.Body)"的约定补常量并把 `createTask` 接入 `decodeJSON`(空 body 400「请求正文为空」、超限 413「请求正文过大」)。该测试现已通过。

## 代码审核(cc-switch-kimi-for-coding/k3.1-pro)

结论:初版不可合入(1 blocker + 3 major)→ 已全部修复后构建通过。

| # | 级别 | 发现 | 处置 |
|---|---|---|---|
| 1 | blocker | 未使用的 `hasMore` state 触发 lint | 删 state 仅留 `hasMoreRef` |
| 2 | major | `loadEarlier` 无 supersede 防护,任务 A 翻页响应可污染任务 B | `taskIdRef` + boot token 三重防护;boot 重置 `loadingMoreRef` |
| 3 | major | 切换任务不重置筛选,旧 worker 集合可使新时间线永久空白 | boot effect 重置筛选 + `key={id}` 重挂载 |
| 4 | major | `Transcript` `byToolId` 仅按 `tool_use_id` 配对,合并 lane 后跨 worker 重复 id 错配 | 配对键加 worker 作用域 |
| 5 | minor | 钉底未覆盖筛选变化/懒加载高度变化 | effect 依赖补筛选 + `ResizeObserver` 兜底 |
| 6 | minor | `AnswerBlock`/`UserRow` 无 worker chip | 加 `showWorker` prop 渲染 chip |
| 7 | nit | chip tooltip 与多选行为不符 | 按状态分文案 |

审核确认无问题:`session=all` 零值过滤、历史游标语义、SSE `since` 重连回放衔接、`createTask` 新错误语义无调用方依赖。

## 验证结果(<DEPLOY_HOST_A> 真实环境)

部署:`/opt/artex` 换装 `0.1.6-trajectory-test`(sha256 校验,start.sh 守护,旧二进制备份 `artex.bak-trajectory-20261113-221949`)。

| 项 | 结果 |
|---|---|
| `TestActivityHistoryAll` 经 SSH 隧道打远端真实 PG | PASS(1.4s) |
| `session=all` 任务历史 API | 22 步升序合并,mainagent/planner/work#1/system 齐全,`has_more`/`earliest_cursor` 正确 |
| 向上翻页 `before=<中间 seq>` | 只回更旧页 ✅ |
| 回归:`main`/`plan`/`intent:<id>` 200;`bogus`/`main:-1`/`intent:xyz` 400 | ✅ |
| SSE 流(since=0) | 持续推送 planner usage/result/text 帧 ✅ |
| UI(已完成任务) | worker 彩色 chip、【推理】块展开全文、tool_use/result 配对(✓/失败)、筛选行(Agent chips 带步数 + 5 类类型开关)、空态/离线指示 ✅ |
| 筛选交互 | 点「规划」→ 只剩 planner 行;关「思考」→ 推理块消失、工具行保留 ✅ |
| UI(运行中任务) | 「实时」指示 + 「实时流式中…」动画,底部实时追加 ✅ |

注意:同源 SSE 靠登录 cookie(`artex_token`)鉴权(EventSource 无法带 Authorization 头);只注 localStorage 会 401,属测试手法问题而非代码缺陷。

## 运维备注

- 回滚:`/opt/artex` 恢复 `artex.bak-trajectory-20261113-221949` 后重启即可。
- 测试期间在远端库创建的测试任务已删除(`DELETE /api/tasks/3`)。

---

# 第三部分:可视化增强落地(2026-11-14,minimap + 详情抽屉 + 意图归属)

方案选型见 `docs/轨迹可视化方案-demo.html`(五方案对比)与 `docs/轨迹Minimap-artex风格-demo.html`(交互定稿);最终落地 minimap 方案,零后端改动。

## 变更清单

| 文件 | 变更 |
|---|---|
| `web/src/components/transcript.tsx` | `ExecView` 每个分组根元素包 `data-seq` 锚点(=组首条 seq,供跳转/高亮);`ToolBlock`/`MessageBlock`/`AnswerBlock`/`UserRow` 在混排视图新增 `#intent_id` 归属标签(`intentTag`);导出 `workerColor` |
| `web/src/app/(main)/function/tasks/detail/_tabs/trajectory-minimap.tsx` | 新。密度导航条:色块=步骤类型,视口指示框(scroll/ResizeObserver 直接写 style 不重渲染),点色块跳转+开详情,点空白比例滚动,被筛步骤变暗,实时边缘线 |
| `web/src/app/(main)/function/tasks/detail/_tabs/trajectory-drawer.tsx` | 新。步骤详情抽屉:思考全文/工具入参(JSON 美化)+配对 tool_result 输出/终答全文(经 `api.activityDetail` 懒加载),元信息含时间/执行者/工具/所属意图(点击即过滤)/token |
| `web/src/app/(main)/function/tasks/detail/_tabs/trajectory-tab.tsx` | 意图归属条(`api.intents` 标题/状态 + `Activity.intent_id` 聚合执行 Worker 与步数,点击过滤,SSE 帧 10s 节流刷新);`passFilter` 谓词主时间线与 minimap 共用;行点击/锚点跳转/选中高亮(`.traj-selected`);集成 minimap 与抽屉 |
| `web/src/app/globals.css` | `.traj-selected` 选中锚点高亮(primary 左边条 + 6% 底色) |
| `web/src/lib/mock/handler.ts` | 补 `/exploration/activity/history` mock(轨迹页此前在 mock 模式无法加载) |

## 数据链路(全部既有接口)

- minimap 色块 / 筛选:`session=all` 历史 + SSE(同第一部分)。
- 意图归属:`Activity.intent_id` 聚合执行 Worker;标题/状态取自 `GET /api/exploration/intents`(TaskNode.payload.summary)。
- 抽屉全文:`GET /api/exploration/activity/{seq}`;tool_use 配对输出按 `worker + tool_use_id` 在已加载 items 内查找。

## 验证

- `biome lint`(改动 4 文件,error 级清零)、`npm run build` 通过。
- `NEXT_PUBLIC_MOCK=1` dev server 实测:意图归属条 13 条意图(标题/Worker chip/状态/步数);minimap 149 色块渲染、点色块跳转居中 + 抽屉打开(思考全文、hashcat 入参+配对输出、所属意图 `#i12`);点意图 chip `#i5` → 时间线收敛为 5 组、141/149 色块变暗、「清除意图过滤」出现;行内 `#intent_id` 标签、选中高亮均正常,页面 0 报错。

## 部署记录(可视化增强,<DEPLOY_HOST_A>:8787)

- 本地 `NEXT_EXPORT=1 next build` → `web/out` 覆盖 `server/webui/dist` → `GOOS=linux go build -tags embedui`,`main.version=0.1.7-trajectory-viz`。
- scp 至 `/opt/artex/artex.new`,sha256 双端一致(`7743552b…9532`);旧二进制备份 `artex.bak-minimap-<ts>`;杀守护+子进程后换装,`nohup ./start.sh` 重启。
- 验证:`/api/health` 返回 `0.1.7-trajectory-viz`;`/login` 200;`session=all` 路由在线(未鉴权 401);线上 JS chunk 含「轨迹缩略导航/清除意图过滤」新代码。登录态 UI 冒烟待有凭据时补。
- 回滚:恢复 `artex.bak-minimap-<ts>` 为 `artex` 后重启 start.sh。

### 视觉精修(2026-11-14 第二轮,0.1.7-trajectory-viz2)

- minimap 改中轴导轨式:1px 中轴线 + 居中圆点(宽度按类型分档:结果/用户/意图 16px、工具/思考 13px、文本 10px,轮次为全宽刻度线),选中圆点 primary 描边 + 微投影,视口框收窄为圆角细框,实时边缘线加发光。
- 意图归属条紧凑化:chip 高 24px、状态 emoji 改为状态圆点(done=绿/running=蓝脉冲/open=灰/proposed=琥珀/失败=红),完整状态文案移入 title;激活态统一为 primary 风格(与筛选 chip 一致),清除按钮改虚线框。
- 详情抽屉精细化:头部加类型色点(与 minimap 同源色)、元信息改 3.5rem 键宽两列网格、分节标题改「小字 + 延展分隔线」、代码块加边框与圆角。
- 部署:`0.1.7-trajectory-viz2`,同第一轮流程,旧二进制备份 `artex.bak-minimap2-<ts>`。

---

# 第四部分:结果块折叠(2026-11-16)

用户反馈:轨迹时间线里规划者的规划结果(如 #919 的 result 步骤)整块 markdown 常开,把后续时间线顶出屏幕;思考/工具/文本块均已折叠,唯独 result 不折叠。

## 变更

| 文件 | 变更 |
|---|---|
| `web/src/components/transcript.tsx` | `AnswerBlock` 新增 `collapsible` prop:true 时默认折叠为一行(chevron + 旗标 + worker chip + `#intent` 标签 + 截断摘要 + 右侧「总结」提示),点击展开全文(markdown/错误文本)+ 时间;折叠态不发 detail 请求,展开时经 `useInView` + `open` 双条件懒加载。`ExecView` 按 `collapsible={!chat}` 传入——对话页(main agent 回答)保持全文常开不变,轨迹页与会话页非 main lane 的所有 result 块默认折叠。 |

零后端改动;`groupSteps` 的 answer 分派不变,minimap 色块/抽屉定位(`data-seq` 锚点)不受折叠影响。

## 验证

- `npx tsc --noEmit`、`npm run build` 通过;biome 对改动行无新增诊断。
- `NEXT_PUBLIC_MOCK=1` dev server 实测(t-acme-web 轨迹页):11 个 result 行默认折叠;点开一行 → markdown 全文 + 时间渲染、右侧抽屉同步打开;再点 → 收起,恢复 11 行折叠态。
- 对话页(主 Agent 会话)回归:5 个 answer 块全部全文常开,无折叠按钮。

## 部署记录(<DEPLOY_HOST_B>:8787)

- 该机此前为基线 `0.1.6`(无轨迹功能);本次随折叠改动一并同步当前工作区(轨迹 Tab + minimap/抽屉 + 本折叠变更),无迁移、零 db 改动。
- 流程:`NEXT_EXPORT=1 npx next build`(npm script 内联 env 在 Windows cmd 不生效,需 bash 设变量)→ `web/out` 覆盖 `server/webui/dist` → `GOOS=linux GOARCH=amd64 go build -tags embedui -ldflags "-X main.version=0.1.7-traj-fold" ./cmd/artex` → scp。
- sha256 双端一致(`0bb3ba0f…f20e8e`);按 `pgrep -x artex` 精确停旧进程(PID 1409294),备份 `artex.bak-trajfold-20261116-013105`,`nohup ./start.sh` 重启。
- 验证:`/api/health` → `0.1.7-traj-fold`;`/login` 200;二进制内含「展开结果全文」新 UI 代码;`session=all` history/stream 路由未鉴权 401(在线)。
- 回滚:恢复 `artex.bak-trajfold-20261116-013105` 为 `artex` 后重启 start.sh。

---

# 第五部分:执行者 chip 常显 + 中文标签(2026-11-16)

用户反馈:任务 11(信息收集 · <TARGET_DOMAIN>)的轨迹里看不出 planner/worker 谁在调工具、谁在说话。根因:库里该任务仅有 planner 一种 worker(21 步,尚未派发 work),而 `ExecView` 的 `showWorker = worker 种类 > 1` 才显示归属 chip——单 lane 时全部行无归属标识;且 chip 显示英文原名(`planner`),与筛选行中文标签不一致。

## 变更

| 文件 | 变更 |
|---|---|
| `web/src/components/transcript.tsx` | `Transcript`/`ExecView` 新增 `showWorkers` prop:true 时每行强制显示执行者 chip(轨迹页传入);缺省仍为"仅混排多 agent 时显示",对话页/会话页行为不变。`chip` 字符串助手组件化为 `WorkerChip`(lane 色 + 中文短标签,title=worker 原名);导出共享 `workerLabel`(主 Agent/规划/审核/Worker N/系统/用户,空→未知)。 |
| `web/src/app/(main)/function/tasks/detail/_tabs/trajectory-tab.tsx` | `<Transcript … showWorkers />`;删除本地 `workerLabel`,改用 transcript 共享映射(筛选行/意图条/抽屉同步受益,补齐 system→系统、user→用户)。 |

零后端改动。

## 验证

- `tsc --noEmit`、`NEXT_EXPORT=1 next build` 通过;biome 无新增 error。
- mock 实测(t-acme-web):轨迹页 planner 行 22 个「规划」chip、work#1 行 5 个 chip;只筛「规划」(模拟任务 11 单 lane)后 22 个 chip 保留——旧逻辑此场景 chip 全隐;对话页混排行为不变(chip 仅混排时出现,标签改中文)。
- 提交:e4fa0084(结果块折叠)、ff0069a2/c2aeaecc(本节;轨迹功能主体仍在工作区未提交,部署以工作区为准)。

## 部署记录(<DEPLOY_HOST_B>:8787,第二轮)

- 同第一轮流程,版本 `0.1.7-traj-chip`,sha256 双端一致(`911d5819…4cd975`);旧进程 PID 1492578 按名精确停掉,备份 `artex.bak-trajchip-20261116-021104`,`nohup ./start.sh` 重启。
- 验证:`/api/health` → `0.1.7-traj-chip`;二进制含本轮嵌入前端(「展开结果全文」标记在位)。
- 回滚:恢复 `artex.bak-trajchip-20261116-021104` 为 `artex` 后重启 start.sh。
- 登录态真机 UI 冒烟仍缺凭据,待有账号后在任务 11 页面直接确认:每行应出现彩色「规划」chip,悬停显示 worker 原名。
