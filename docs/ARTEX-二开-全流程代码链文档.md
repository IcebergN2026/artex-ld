# ARTEX 二开 · 全流程代码链文档（核心流程详解版）

> 仓库根：发布包根目录（模块 `github.com/Autumn-27/artex`）
> 阅读约定：本文使用文件 + 函数/语句锚点，不再把容易漂移的数字行号当作当前事实；关键函数索引见附录。
> 流程图一律 ASCII；每步都给出「发生什么 → 在哪段代码」。
---

## 目录

- 第 1 章 · 正常任务全流程（goal → planner → worker → goal）
- 第 2 章 · 审核全流程（intent 三形态 → 送审 → 三通道 → ask 卡 → 驳回回流 → 三层互斥）
- 第 3 章 · planner 轮详解（唤醒事件 → 门禁 → 情境注入 → 提示词 → 意图产出 → goal_met）
- 第 4 章 · 种子意图与自定义 agent 执行链（pre_agent / executor 路由）
- 第 5 章 · finding 触发自定义 agent（trigger → 会话 → 工具回流）
- 第 6 章 · skill / tool / MCP 在任务中的配合
- 第 7 章 · 全项目门禁总表
- 附录 · 关键函数索引

---

# 第 1 章 · 正常任务全流程

## 1.0 全景 ASCII 图

```
HTTP POST /api/tasks                    server/server.go 路由 →  createTask
  │ 校验（profile/worker_count/pre_agent/提示词三级快照）
  ▼
CreateTaskWithOptions 单事务             server/server.go → manager.go → db/tasks.go
  │ 建 exploration 容器  → 播 origin fact  → 插 tasks 行
  ▼
launchTask                              server/server.go → goals.go
  │ seed 根资产  → seedFirstIntent （可选）→ admitTask
  ▼
startTaskEngine                         goals.go
  │ createGoals （LLM 分解目标，失败回退单目标 ）→ engine.Run
  ▼
Engine.Run                              engine.go
  │ plannerLoop ×1  + workerLoop ×N  + deadline
  ▼
            ┌──────────── plannerLoop 门禁链  ────────────┐
 唤醒源 ──▶ │ debounce  → 五道闸  → 待审抑制   │ ──▶ planner.Plan
(worker完成/ │ → goalless  → drainTriggers             │      │ 四级提示词+工具循环
 人工/心跳/  └─────────────────────────────────────────────────┘      ▼
 审核结论)                                                       add_intent
                                                                  │ createIntent 内核
                                                                  ▼
                                              AddIntent(open) db/exploration.go
                                                                  │ state='open' 进 Frontier
              ┌─────────── workerLoop 五道前门  ───────────┐    ▼
              │ claimNext  → Frontier(20)             │──▶ ClaimIntent CAS
              └───────────────────────────────────────────────┘    │ state open→running
                                                                    ▼
                                              runIntent （executor 路由 ）
                                                                    ▼
                                              worker.Execute  / RunCustomExecutor
                                                                    │ 产出 fact/finding + yields 边
                                                                    ▼
                                              终态分流  → NotifyDone
                                                                    │ TriggerEvent{done}
                                                                    ▼
                                              下一轮 planner … prove_goal  / goal_met
                                                                    │ tsx.GoalMet=true
                                                                    ▼
                                              engine  met → SetTaskStatus done
                                              → cancelExec(AbortGoalMet)
```

**不变量**：一次唤醒 ≠ 一次事件。事件本体在 `pendingTriggers`（`server/manager.go`），
`notify` chan 只是边沿信号（`server/manager.go`，cap 1）；真实消费点只有三处：
`engine.go`、`engine.go`、`engine_timeout.go`（均调 `drainTriggers`，函数体 `server/manager.go`）。

## 1.1 任务创建链（HTTP → DB）

```
客户端 ──POST /api/tasks(JSON)──▶ server.go 路由 ──▶  createTask(w, r)
                                        │
                                        ▼ createTask 内部（每步 = 一道校验门禁，失败即 400）
   ┌──────────────────────────────────────────────────────────────────────────┐
   │  Decode JSON（失败 400 bad json）                                     │
   │  Description 空 → 补「未命名任务」                                     │
   │  validateTaskProfileIDs（id≤0/重复/未配 Key → 400）                    │
   │  TimeoutSeconds<0 → 归零                                              │
   │  SourceTaskIDs 超 MaxTaskSourceCount → 400                            │
   │  NormalizeTaskCompanyIDs                                              │
   │  reviewEnabled = req.ReviewEnabled!=nil && *req.ReviewEnabled          │
   │  分类预设快照（CategoryID → 默认模板/AssetMode/描述/审核开关）            │
   │  assetMode 归一（"pentest" 兜底，>64 字符 400）                        │
   │  buildTaskInterceptRules                                              │
   │  pre_agent 三层校验（存在 && Enabled && !Builtin，否则 400）   ← 第 3 章  │
   │  planner 提示词三级快照链（手改→模板→默认，见下方数据格式）                │
   │  worker_count ∈ [0,16]（0=跟随全局）                                  │
   └──────────────────────────┬───────────────────────────────────────────────┘
                              ▼
               s.m.CreateTaskWithOptions(desc, goal, TaskCreateOptions{...})
                              │  （PreAgent/PlannerPrompt/PlannerTemplateID/
                              │    WorkerCount/ReviewEnabled/AssetMode 入参打包）
                              ▼
              manager.go Manager.CreateTaskWithOptions
                              │
                              ▼ 单事务 tx（db/tasks.go DB.CreateTaskWithOptions）
        ┌──────────────────────────────────────────────────────────────┐
        │  INSERT explorations(description, goal) RETURNING id     │
        │        → exploration_id（此后所有节点都挂这个容器）              │
        │  INSERT exploration_nodes                                │
        │        kind='fact', state='origin', origin='system', prio=0  │
        │        → origin fact 根节点（所有无父意图的 derived_from 挂点）  │
        │  INSERT tasks(planner_prompt_override, worker_count,     │
        │        review_enabled, asset_mode, ...) RETURNING id         │
        └──────────────────────────┬───────────────────────────────────┘
                                   ▼
        insertTaskRelations / insertTaskCompanies / insertTaskLLMProfiles
                                   │
                                   ▼ 返回 *Task ──▶  launchTask（见 1.2）
```

**planner 提示词三级快照链**（—，逐级回退，一旦命中即定格）：

```
①  手改 planner_prompt ── validatePlannerTemplateBody（server/planner_templates.go）
     │  校验三连：空→报错 ／ >32KB(MaxPlannerTemplateBodyBytes)→报错
     │           ／ template.Option("missingkey=error") + 全字段 PlannerVars 试渲染
     ├─ 通过 → plannerPrompt = 手改文本 ──────────────────────────┐
     └─ 失败/为空 → 400（手改路径是硬错误，不回退）                │
②  req.PlannerTemplateID > 0 → pg.GetPlannerTemplate          │
     │  nil → 400 并列出可用模板                                   │
     ├─ 命中 → plannerPrompt = tpl.Body ──────────────────────────┤
     └─ 未传 → ③ pg.DefaultPlannerTemplateBody()（db/config.go）│
                                                              ▼
               无论哪级命中，正文都以【快照】写入 tasks.planner_prompt_override
              → 此后模板库增删改均不影响本任务（K22 快照语义）
```

**方法简介**：

| 方法 | 位置 | 简介 |
|---|---|---|
| `Server.createTask` | `server/server.go` | 建任务 HTTP 入口；全部入参校验 + 提示词快照 |
| `validatePlannerTemplateBody` | `server/planner_templates.go` | 模板正文校验（32KB/missingkey=error 试渲染） |
| `Manager.CreateTaskWithOptions` | `server/manager.go` | 透传 DB 单事务建任务 |
| `DB.CreateTaskWithOptions` | `db/tasks.go` | 容器+根 fact+tasks 行三 INSERT 同事务 |

## 1.2 启动链（seed → 准入 → 拆目标 → 起引擎）

```
server/server.go launchTask(t, seedText, seedFirstIntent)      【同步执行】
  │
  ├─▶ goals.go  beginTaskOperation 门（并发操作互斥）
  │
  ├─▶ goals.go  s.seed(t, seedText)               server/server.go Server.seed
  │                    │  parseTarget 解析目标文本
  │                    │  → UpsertIP / UpsertHTTPService / UpsertRootDomain（资产图入库）
  │                    │  → SetTaskAssetSource
  │                    │  →  OriginFactID() + t.Store.Anchor(begin, rootID)
  │                    │  （注释明确「不在这里 Notify」——唤醒统一走 admit 之后）
  │
  ├─▶ goals.go  s.seedFirstIntent(t)（可选；pre_agent≠"" 时写入 executor，见第 3 章）
  │
  ├─▶ goals.go  decInflight
  │
  └─▶ goals.go  s.admitTask(t, "bootstrap")
                     │
                     ▼ goals.go admitTask →  admitTaskWhen
       ┌────────────────────────────────────────────────────────┐
       │ 并发度三判据：                                          │
       │    readyBacklog  （全局有排队积压？）                │
       │    atCapacity    （达到全局并发上限？）              │
       │    shouldQueue = enabled && (!ready || backlog ||  │
       │                    capacity)                           │
       └──────────────┬───────────────────────────────────────┘
                 shouldQueue?
            是 ──────┴────── 否
            ▼                ▼
   goals.go        goals.go
   engine.Pause        startAdmittedTask
   (queued_for_       │ bootstrap 分支 ：
    admission)        │   beginTaskOperation → Resume
   挂入等待队列        │   → go func { startTaskEngine }  ←★唯一的异步点
            │        │ 非 bootstrap ：
            │        │   Run + Resume + startDeadlineCoordinator
            │        ▼
            └─────▶ goals.go startTaskEngine  【先拆目标、后起引擎】
                       │
                       ├─  execContextFor + 早退
                       ├─  emit「第 0 轮目标拆解」
                       ├─  createGoals(ctx, t, emit)
                       │      │
                       │      ├─  agent.DecomposeGoalsWithProvider(...)
                       │      │     ← LLM 把任务目标拆成 goal 节点词表
                       │      │     （含 vulnclass 标注，agent/goals.go）
                       │      └─ 无产出回退 ：
                       │         AddNode(KindGoal, state='open')
                       │         + Link(origin, spawns, id)  单目标兜底
                       ├─  逐条 emit 目标文本（VulnClass 非空加 [前缀]）
                       └─  s.engine.Run(s.ctx, t)   ← engine 协程拓扑见 1.3.1
```

**方法简介**：

| 方法 | 位置 | 简介 |
|---|---|---|
| `Server.launchTask` | `server/server.go` | 任务启动总入口：seed 资产→种子意图→准入 |
| `Server.seed` | `server/server.go` | 目标文本 → 资产图入库 + 根锚点 |
| `Manager.admitTask/admitTaskWhen` | `server/goals.go` | 准入/排队唯一路径；排队先 Pause 装屏障 |
| `Manager.startAdmittedTask` | `server/goals.go` | bootstrap 异步起引擎 / 非 bootstrap 直接 Run |
| `Manager.startTaskEngine` | `server/goals.go` | 先 LLM 拆目标，后 `engine.Run` |
| `Manager.createGoals` | `server/goals.go` | 目标分解；失败回退单 goal |
| `agent.DecomposeGoalsWithProvider` | `agent/goals.go`( 调用侧组装) | LLM 目标分解器（产出 text+vulnclass 词表） |

## 1.3 Engine 调度链

### 1.3.1 Engine.Run 协程拓扑（`server/engine.go`）

```
  workers := e.m.Workers()                    全局默认 worker 并发
  if t.WorkerCount > 0 { workers = t.WorkerCount }
      ↑ 任务级覆盖（仅启动时读一次，运行中改 worker_count 不生效）
  IsDeleting 早退
  幂等门 started.LoadOrStore(t.ID, true)
      └─ 已跑过 → 只补 t.Notify() 后直接 return
  rt := registerTaskRoutine(ctx, t.ID, 1+workers)   协程登记（可整体取消）
      ├─  runTaskRoutine(rt, plannerLoop)           planner × 1
      ├─  for i:=1..workers:
      │            runTaskRoutine(rt, workerLoop, "work#i")   worker × N
      └─  startDeadlineCoordinator(ctx, t)          超时定时器
  首轮 kick 守卫：
      HasActiveIntent()==false && HasProposedIntent()==false ──▶ t.Notify()
      （有待审意图 → 跳过 kick，等人工放行/驳回后自然唤醒）

运行期拓扑：
┌─────────────┐   notify chan    ┌────────────────┐
│ plannerLoop │◀────(cap 1)─────▶│  Task(t)        │
│  (×1)       │                  │  pendingTriggers│←── worker 完成/人工/心跳/
├─────────────┤                  │  ([]TriggerEvent)│    审核结论/工具回调
│ workerLoop  │◀─────────────────┤  notify chan    │
│  (×N)       │                  └────────────────┘
└─────────────┘
```

### 1.3.2 plannerLoop 门禁链（`engine.go`，顺序即语义；逐行行号见 §7.4）

```
engine.go plannerLoop(ctx, t)
  │  interval = plannerHeartbeatInterval(t)（下限 db.MinPlanHeartbeatSeconds=600s ）
  │  heartbeat := time.NewTimer(interval)          ← 心跳臂
  │
  │  runRound(src) 闭包  ← notify/heartbeat 两臂共用
  │  │
  │  │   debounce timer 起
  │  │   drain 循环：select{ <-t.notify 重置timer / <-timer.C 放行 }
  │  │        ↑ 突发多次 Notify 合并成一次唤醒
  │  │
  │  │  ╔═══════ 门禁链（每道不过即 return，数字=门禁顺序）═══════╗
  │  │  ║  1  snapshotFor nil → 空转（LLM 未就绪）        ║
  │  │  ║  2  IsPaused → return（排队/人工暂停）          ║
  │  │  ║  3  IsDeleting → return                         ║
  │  │  ║  4  isTerminalStatus → return（任务已有终态）     ║
  │  │  ║  5  isSettling → return（超时收尾进行中）         ║
  │  │  ║  6  待审抑制：HasProposedIntent()                   ║
  │  │  ║       ├─  log「planner 轮抑制：有待审意图」            ║
  │  │  ║       └─  drainTriggersRetaining(                   ║
  │  │  ║             "revision_requested","hint") → return        ║
  │  │  ║          （丢弃可再生的 done/finding；保留驳回修订+人hint） ║
  │  │  ║  7  goalless 待审豁免：再次 HasProposedIntent    ║
  │  │  ║       → log「goalless 收尾豁免」→ return 保持 running     ║
  │  │  ║  8  goalless 判定（rejected 例外）：              ║
  │  │  ║     无 open 目标 && 无 rejected                            ║
  │  │  ║     ├─  drainTriggers() 丢累积 done/finding           ║
  │  │  ║     ├─  HasActiveIntent 假                            ║
  │  │  ║     │     →  SetTaskStatusGuarded("done")            ║
  │  │  ║     │     →  emit「目标已全部达成…任务结束」        ║
  │  │  ║     └─  return（goalless 分支永不进 Plan）            ║
  │  │  ║     有 rejected → 放行 planner 轮（否则驳回稿永远修不了）   ║
  │  │  ╚══════════════════════════════════════════════════════════╝
  │  │
  │  │  全部门禁通过：
  │  │   9  beginTaskOperation + defer decInflight（并发操作计数）
  │  │  10       stampFirstRun（首跑才起算超时，engine_timeout.go）
  │  │  11   clockCtx（pause/delete 可取消 + deadline 夹逼）
  │  │  12   emit「第 N 轮规划」
  │  │  13       triggers := t.drainTriggers()   ← 取走事件本体
  │  │  14       met, reason, err := planner.Plan(...)
  │  │                  （ BeginLLMCall /  EndLLMCall 包裹）
  │  │
  │  │  结果分流（switch ）：
  │  │    err!=nil → log 规划出错
  │  │    met →
  │  │         HasRejectedIntent() 守卫 ──有驳回稿──▶ 只 log
  目标达成判定被拦截──┐                              「目标达成判定被拦截」，不落 done
  │  │        │（无驳回稿）│                            │
  │  │        ▼          │                            │
  │  │    SetTaskStatus("done")            任务保持 running
  │  │        （manager.go → db/tasks.go，completed_at COALESCE）
  │  │    cancelExec(AbortGoalMet)（engine.go）
  │  │        → 在跑 worker 的 ectx 被取消 → 走 1.3.4  落 stopped
  │  │    default → log「规划完成」
  │  │
  │  ├─  case <-t.notify: runRound("edge")      ← 边沿唤醒臂
  │  └─  case <-heartbeat.C:
  │        抑制 ：无活跃意图 && 仅有待审意图
  │                 → log「planner 心跳抑制」+ continue（心跳空转无意义）
  │        否则  runRound("heartbeat")
  │
  │  终局支路（不经本循环）：
  │  engine_timeout.go runFinalPlannerRound
  │    绕过 guard/debounce/待审抑制，直驱 planner.Plan
```

**唤醒事件数据格式**（`agent/planner.go`）：

```go
type TriggerEvent struct {
    Kind     string   // "done"|"finding"|"goal"|"hint"|"goal_deleted"|"goal_edited"|"cancelled"|"revision_requested"
    IntentID int64    // 关联意图号（goal/hint 类为 0）
    Detail   string   // 主文本（驳回理由/finding 摘要…）
    Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要
    Goals    []string // Kind=="goal" 专用：新增目标文本
    OldGoal  string   // Kind=="goal_edited" 专用
    NewGoal  string   // Kind=="goal_edited" 专用
    Hints    []string // Kind=="hint" 专用：新增提示文本
}
```

生产者全集（`server/manager.go`）：`NotifyDone ` / `NotifyFinding ` / `NotifyGoal ` /
`NotifyHint ` / `NotifyRevisionRequested ` / `NotifyGoalDeleted ` / `NotifyGoalEdited ` /
`NotifyCancelled `。排空函数：`drainTriggers` `manager.go`、`drainTriggersRetaining` `manager.go`。

**方法简介**：

| 方法 | 位置 | 简介 |
|---|---|---|
| `Engine.Run` | `server/engine.go` | 起 planner×1+worker×N+超时定时器；幂等门 |
| `Engine.plannerLoop` | `server/engine.go` | 规划轮主循环：debounce→门禁链→Plan→分流 |
| `Engine.snapshotFor` | `server/engine.go`（plannerLoop 调用） | 取任务当前 planner/worker 装配（LLM 未就绪返回 nil） |
| `Task.Notify` 族 | `server/manager.go` | 事件入 pendingTriggers + notify chan 边沿 |
| `Task.drainTriggers[Retaining]` | `server/manager.go` | 消费事件本体（Retaining 版按 Kind 过滤保留） |

### 1.3.3 workerLoop 领取链（`engine.go`）

```
五道前门（各带 sleep 退避）：
  1.   snapshotFor nil → sleep 1500ms
  2.  IsPaused → sleep 1000ms
  3.  IsDeleting → return
  4.  isSettling → sleep 1000ms（超时收尾中不再领新意图）
  5.  isTerminalStatus → sleep 1000ms
 beginTaskOperation →  runWorkerStep → 未领取 sleep 800ms

runWorkerStep：
   intent := e.claimNext(t, name)
   return e.runIntent(ctx, t, name, worker, intent, "", "")

claimNext：
   fr, _ := t.Store.Frontier(20)          按 priority DESC, id ASC
   for _, in := range fr { ClaimIntent(in.ID, name) }  抢到一个即返回

Frontier SQL（db/exploration.go，WHERE ）：
  SELECT ... FROM exploration_nodes
  WHERE exploration_id=$1 AND kind='intent' AND state='open'
  ORDER BY priority DESC, id ASC LIMIT $2

ClaimIntent CAS（db/exploration.go）——open→running 唯一原子变迁点：
  UPDATE exploration_nodes SET state='running', owner=$1
  WHERE id=$2 AND exploration_id=$3 AND kind='intent' AND state='open'
  （ RowsAffected()==1 才算抢到；多 worker 竞争靠这一行）
```

### 1.3.4 runIntent 与终态分流（`engine.go`）

```
 stampFirstRun
 clockCtx
 已取消 → transitionIntentState(running→open) 回退后 return
 workCtx, WithCancelCause + registerWork（kill_work 只杀这一条）
 intercept.WithTaskContext(label="work#N · #intentID") ← K15 来源标识（≠"review" 不弹卡）
 steerHooks（steer 注入 + 空转续跑额度）
 payload.executor 唯一读取点（见第 3 章路由）
 model_error 重跑循环

终态分流表：

| 终态 | 位置 | 条件 | 变迁 |
|---|---|---|---|
| 用户 pause |  | action=="pause" | running → paused |
| 用户 cancel |  | action=="cancel"（取消栅栏，防幽灵 running） | running → paused |
| 任务暂停 |  | 任务 pause 取消执行 | running → open（resume 后重领） |
| 超时收尾 |  | isSettling | running → exhausted |
| 任务终态 |  | 任务已 done/failed/timeout | running → stopped |
| planner kill |  | workCtx 取消而 ectx 存活 | running → stopped + Notify() |
| 正常分流 |  | state="done"； err→"blocked"； max_turns/timeout→"exhausted" | |
| 配额耗尽 |  | blocked && LLM 链耗尽 | 写 blocked_reason 不动 state |
| 落库 |  | transitionIntentState（engine.go → db/exploration.go CAS，终态清 blocked_reason + completed_at） | |
| 回流唤醒 |  | t.NotifyDone(intent.ID) → TriggerEvent{done} | 下一轮 planner |
```

`transitionIntentState` 的 CAS SQL（`db/exploration.go`）：

```sql
UPDATE exploration_nodes
SET state=$1, blocked_reason=NULL, content_version=content_version+1,
    completed_at = CASE WHEN $5 THEN now() ELSE NULL END
WHERE id=$2 AND exploration_id=$3 AND kind='intent' AND state=$4
```

## 1.4 planner 轮内部（`agent/planner.go Planner.Plan`）

```
  compactor.OnPlannerRound（冷节点压缩推进）
  tsx := NewToolSet(ts, "planner")
 注入 FindingRecorder/AssetStore/TaskID/Coverage/killWork/steerWork
 审核三回调：SetNotifyReview / SetReviewEnabled(闭包 taskID) / SetExecutorChecker
  SetOwnerNode(origin fact)
  base := DropCoverageTools(PlannerTools()) + DefaultTools()
  WithRunInfo →  AugmentTools（技能/MCP 增强，见第 5 章）
  situational := renderTriggers(ts, triggers) + renderGraphOverview(graphOverviewData())
 终局模式拼 wrapup 指令
  ensureRunDir； intercept.WithReviewContext
  sysBody := plannerSystem(...)（四级链见 1.4.2）
 审核开启追加「必须用 add_review_intent」
  constraintBlock； deferredSystem(sysBody, def)
 agentcore.Options（MaxTurns  / MaxDuration  / Compaction  /
      Todos  / Settlement  / NonStreaming  / MaxTokens ）
 SessionID = "exp<id>-planner"（每任务累积会话）
 开场白三分流：有 trigger→「刚有具体变动」；无 trigger→定时巡检；
      无活跃意图→「必须产出 ≥1 意图」硬底线
  captureRun(ctx, opts, input, emit)   ← LLM+工具循环入口（agent/capture.go）
  return tsx.GoalMet, tsx.Reason, err   ← 收官结论由写工具置位
```

**关键事实**：`Plan()` 全函数无 `SetNotify/SetNotifyHint` 注入（仅 `SetNotifyReview` 唤醒审核轮）——
planner 不自唤醒；下一轮永远由 worker 完成/人工/心跳/审核结论从 engine 侧边沿触发。

### 1.4.1 renderTriggers 分支（`agent/planner.go`）

| Kind | 位置 | 文案要点 | 生产者 |
|---|---|---|---|
| `goal` / `hint` / `goal_deleted` / `goal_edited` | — | 人工增删改目标/战略提示 | `server/manager.go` |
| `finding` | — | 「意图 #%d 的 worker 报告了 finding」 | ` NotifyFinding` |
| `cancelled` | — | 摘要优先取 ev.Summary（节点已删） | ` NotifyCancelled` |
| `revision_requested` | — | 「意图 #%d 被审核驳回…请用 **update_intent 修改这条意图本身**…改完会自动重新送审」 | ` NotifyRevisionRequested` |
| `done`（default） | — | worker 结束结论 + factIDsYielded | ` NotifyDone` |

尾注 — 指引 `node_detail / get_worker_output / list_findings` 再查细节。

### 1.4.2 graphOverviewData 态势键（`agent/tools.go`，工具与唤醒提示共用唯一组装点）

- ` goals` / ` hints`（含 `traffic_refs `）
- ` parentsOf`（derived_from+spawns）、` yieldsOf`（yields）
- ` openIntentsCap=30`、` open_intents` / ` running_intents` / ` recent_done_intents`
- ` proposed_intents`（待审，planner 可见但不派单）
- ` rejected_intents`（驳回待修订持久兼底）——即使 `revision_requested` 事件丢失（进程重启），planner 仍能在概览里发现待改稿
- ` done_intents_total` / ` frontier_open` / ` findings_total` / ` facts`

### 1.4.3 plannerSystem 四级提示词链（`agent/planner.go`）

```
①  任务级快照 taskOverride（tasks.planner_prompt_override，每轮现读
        ← server/task_llm.go SetTaskPromptOverride）
②  PromptOverride("planner")（DB agent_prompts 当前正文
        ← server/server.go 接线）
③  PlannerDefaultTemplateBodyFn()（DB planner_templates 表 is_default 行
        ← server/server.go → db/config.go）
④  代码内置 plannerDefaultTmpl（永不空）
每级 renderTmpl(tmpl, PlannerVars{Goal,Scope,AssetSummary,DataDir,Now})（agent/prompt.go，结构体共 5 字段）；**planner 运行期只填 Goal/DataDir/Now，Scope/AssetSummary 为空串（不报错）；模板预览才填完整字段**。
missingkey=error 失败即回退下一级
（log: [planner] plannerSystem 渲染失败 ⚠ 回退下一级：level=N）
```

## 1.5 意图创建链（add_intent 直投）

公共内核 `createIntent`（`agent/tools.go`）：

```
校验 parent_ids 必须是本任务 fact/finding → priority 缺省 5 上限 10
→ anchors := pidList(asset_ids) → CheckAssetsIntercept 资产范围拦截
→ payload 组装：summary + asset_ids + extraPayload 白名单（pending_executor/replaces/review_reason）
→ if proposed { payload["review_status"]="pending"; payload["executor"]="review" }  ← 强制，不可覆盖
→ 落库：AddIntent(open) db/exploration.go / AddProposedIntent
       （单事务插节点 + 逐条 exploration_anchors ON CONFLICT DO NOTHING）
→ 血缘：parent derived_from 边；无 parent 挂 origin fact
```

直投工具 `add_intent`（`agent/tools.go`）：批量循环 `addOneIntent → createIntent(it, false, "", nil)`；
批量返回 `{ids, created, errors}`；`ids` 与输入 `intents` 等长同序（失败项 id=0），`errors` 是索引字符串到错误文本的 map 且仅失败时返回；注册进 `PlannerTools`/`MainAgentTools`。
送审工具 `add_review_intent` 见第 2 章。

## 1.6 worker 执行体（`agent/worker.go Execute →  execute`）

```
  tsx := NewToolSet(ts, name)
  SetFindingRecorder； SetTaskID； SetCoverageEnabled； SetAssetStore
  tsx.SetOwnerNode(intent.ID)（新资产锚定到意图）
  tsx.SetNotifyFinding(notifyFinding)
  base := WorkerTools() + extraTools
  base += defaultToolsExcept("MultiEdit","Glob","Grep")
  WithRunInfo； AugmentTools
  runDir := ensureRunDir(workDir, taskID, intent.ID)
  ctx = intercept.WithReviewContext(...)
  sysBody := workerSystem(proxyAddr, proxyCACert, workDir, runDir)（定义 ）
  constraintBlock； sysBody += RenderIntentTask(intent)（定义 ）
 意图锚定资产块（intentAssetIDs → GetByIDs → JSON + AddAutoScope）
  「开始执行上面这条意图：只做它、只产生事实、assets、finding、做完即停。」
```

产出边：worker 写 fact/finding 时 `Link(intent, yields, fact)`；意图上溯 `derived_from`；
目标派生 `spawns`；证据证目标 `proves`。**本库没有 `produces` 关系**，worker 产出边是 `yields`。

## 1.7 收官链（prove_goal → goal_met → done）

```
prove_goal（agent/tools.go）：
   校验 goal 是 goal 节点、evidence 是 fact/finding
   Link(ev, proves, goal) +  SetNodeState(goal, "met")    ← goal open→met 落点
   全目标扫描 allMet →  t.GoalMet=true（无显式 goal_met 也能收官）

goal_met（agent/tools.go）：
   t.GoalMet = true（无 allMet 校验，直接置位；全局收官工具）

主线：
  planner.go Plan 返回 (tsx.GoalMet, tsx.Reason, err)
  → engine.go →  met →  HasRejectedIntent 守卫
  →  SetTaskStatus("done")（manager.go → db/tasks.go）
  →  cancelExec(AbortGoalMet)（agent/cancelcause.go）

旁路 A goalless（engine.go）：纯 Go 收尾，见 1.3.2 门禁链第 8 条；
  SetTaskStatusGuarded（manager.go → db/tasks.go）：
  UPDATE tasks SET status=$1, completed_at=COALESCE(completed_at, now())
  WHERE id=$2 AND status NOT IN ('done','failed','timeout')   ← 首个终态写者胜

旁路 B 超时收尾（engine_timeout.go settleTask）：
   markSettling 门 →  drain 等待（超时 → cancelExec(AbortSettleDrainTimeout)）
  →  runFinalPlannerRound（终局 Plan 调用 ）→  met 则 status=done 否则 timeout
  →  SetTaskStatusGuarded
```

---

# 第 2 章 · 审核全流程

## 2.0 全景 ASCII 图（送审 → 三通道 → 回流）

```
planner 轮（tasks.review_enabled=true：agent/planner.go 注入强制送审提示）
  │
  └─ add_review_intent                        agent/tools_review.go
       │ addOne  → createIntent(it, true, "", extra)  → agent/tools.go
       │    强制写 payload：review_status="pending"、executor="review"
       │   （proposed=true 时禁止 extraPayload 覆盖 —— 待审闸门双条件）
       ▼
     AddProposedIntent                        db/exploration.go
       │ state='proposed'，origin 默认 "review"
       ▼
     t.wakeReview()                           agent/tools_review.go（批量 ）
       │ agent/tools.go → server/review.go wakeReviewRunner
       ▼
     ReviewRunner.Wake()                      server/review.go（wake chan cap 1，非阻塞）
       ▼
     ReviewRunner.Run                         server/review.go
       │  启动即先 sweep 一次；之后 select{ ctx.Done / wake / tick }
       │ tick = 15s—— 报告里的 60s 已过时
       ▼
     sweep                                    server/review.go
       │ TasksWithProposed（db/tasks.go）→ 逐任务 m.Task → preReviewTask
       ▼
     preReviewTask                            server/review.go
       │  FreshPendingIntents(50)        db/exploration.go（reviewed_version<content_version）
       │  slot.acquire("prescreen", 5min) server/review.go（拿不到就本轮跳过）
       │  prov := rr.reviewProvider(t)   （nil → 整段跳过，见 2.4.4）
       │  t.ReviewAuto ?
       │        ├─ 是 ─▶ autoDecide  → agent.RunReviewAuto（tools_review.go）
       │        │         决策以 JSON 返回 → 由 server 做 CAS
       │        └─ 否 ─▶ agent.RunReviewPreScreen（tools_review.go）
       │                 工具 = ReviewReadOnlyTools（tools_review.go，物理只读 K14）
       ▼
     verdicts（只认 fresh 白名单，verdict ∈ pass|revise）
       ├─ pass    SetPrescreenPassNote → 只写 pre_review='pass' 建议（state 仍 'pending'）
       ├─ revise  SetPrescreenVerdict                                db/exploration.go
       └─  passed+revised>0 → log「预审落账（人工全审）」+ emit 活动
              **不 Notify、不 ApprovePrescreenPass**——人工全审，无任何自动放行

审核意图的三条处置通道（approve/reject 共用 DB 闸门；add_intent 另有直投 open 旁路）：

  ① 机器（仅 ReviewAuto 自动处置）ReviewRunner ─┬─▶ approveIntentAt  db/exploration.go
  ② 人工面板（HTTP）    server.go ─┤   RequestRevisionAt
  ③ 审核对话（LLM+人）  /review/chat  ─┘   （③ 的写工具必过 ask 卡 K15，见 2.6）

落库之后：
  approve → state='open'，executor 由 pending_executor 转正
          → t.Notify()（自动审核、人工 HTTP、审核对话工具；预审不放行）
  reject  → state 仍 'proposed'，review_status='rejected'，content_version+1
          → NotifyRevisionRequested（server/manager.go）
          → planner 下一轮：renderTriggers  文案 + graphOverview.rejected_intents
```

**不变量**：待审池只有一个判据 `kind='intent' AND state='proposed' AND payload->>'executor'='review'
AND review_status='pending'`（`db/exploration.go`）；四处 CAS 落点
（自动 approve、自动 reject、人工 approve/reject、对话工具）全部收敛到
`approveIntentAt` 与 `RequestRevisionAt` 两个函数，**没有任何代码直接用 UPDATE 改这两个状态**。

## 2.1 审核域数据模型（intent 的三种形态）

| 形态 | 判据 | payload 关键字段 | 谁能改 |
|---|---|---|---|
| 待审 | `state='proposed'` + `executor='review'` + `review_status='pending'` | `pending_executor`、`review_reason`、`replaces` | 只有审核通道（2.5） |
| 驳回 | 同上但 `review_status='rejected'` | 追加 `review_note`、`reviewed_version`、`revision_count` | 审核通道写；planner 用 `update_intent` 改内容 |
| 放行 | `state='open'` | `executor`（转正或删除）、`review_status` 已删 | worker/engine（第 1 章 1.3.4） |

字段写入点（全量）：

```
pending_executor   agent/tools_review.go   add_review_intent 的 target_executor 落点
                   db/exploration.go 批准时删除（转正）
review_reason      agent/tools_review.go（送审理由，审计留痕）
replaces           agent/tools_review.go（旧意图存在性先校验）
review_status      agent/tools.go createIntent（送审）；db/exploration.go RequestRevisionAt（驳回）；db/intent_edit.go UpdateIntent（重送）
review_note        db/exploration.go（驳回理由，2000 rune 截断）
                   db/exploration.go SetReviewNoteAt（补注，不推版本）
reviewed_version   db/exploration.go（最近一次被机器看过的版本）
pre_review         db/exploration.go（pass）/ （revise）—— 预审判定留痕
revision_count     db/exploration.go（驳回次数 +1）
```

`reviewed_version` 是「fresh 判据」的唯一来源：`FreshPendingIntents` 只取
`reviewed_version IS NULL OR reviewed_version < content_version`（`db/exploration.go`），
所以 planner 一改稿 `content_version+1`，这条意图就自动重新变成 fresh，无需任何显式入队。

## 2.2 待审单一过滤器与四个哨兵错误

```sql
-- db/exploration.go  proposedPendingCols(alias)  ← 全库唯一「待审」定义
kind='intent'
  AND <p>state='proposed'
  AND <p>payload->>'executor'='review'
  AND COALESCE(<p>payload->>'review_status','pending')='pending'

-- db/exploration.go  rejectedCols（驳回池：同上前三条件 + 第四条反向，无 COALESCE）
kind='intent' AND <p>state='proposed' AND <p>payload->>'executor'='review'
  AND <p>payload->>'review_status'='rejected'
```

`COALESCE(...,'pending')` 的用途：改造前的老行没有 `review_status` 字段，缺省视作待审，
避免历史数据在升级后「隐身」（rejectedCols 不加 COALESCE，因为老行不可能是驳回态）。

| 哨兵错误 | 位置 | 语义 | 对外翻译 |
|---|---|---|---|
| `ErrIntentNotFound` | `db/exploration.go` | 行不存在 / 不属于该任务 | HTTP 409「意图已被处理（并发冲突）」；工具「意图 #%d 不存在」 |
| `ErrNotPendingReview` | `db/exploration.go` | 存在但不是待审三条件 | 同上 |
| `ErrStaleVersion` | `db/exploration.go` | `content_version` 与期望不符 | 同上；`update_intent` 侧提示重新 `list_intents` |
| `ErrNoteRequired` | `db/exploration.go` | 驳回无理由且旧稿也无 `review_note` | 工具「驳回理由必填」 |

工具侧翻译器：`reviewErrText(err, id)`（`agent/tools_review.go`）；
HTTP 侧的映射见 2.7.2。

## 2.3 送审链（add_review_intent）

```
add_review_intent                                 agent/tools_review.go
  │  总开关前置：t.reviewEnabledFn 存在且返回 false
  │           → 错误「该任务未开启意图审核（review_enabled=false），add_review_intent 不可用；确认无需审核时请改用 add_intent 直投」
  │           （闭包来源 agent/planner.go → server/task_llm.go）
  │
  ├─ 批量入参 intents[]: 逐条 addOne(reviewItem)
  │     checkExecutor(TargetExecutor)（server/review.go → engine.resolveExecutor）
  │             失败 →「target_executor %q 不可用：…请改用已注册且启用的自定义 agent，或不传该参数」
  │     replaces 必须指向本任务已存在节点，失败「replaces 旧意图 #%d 不存在」
  │    extra := {pending_executor, replaces, review_reason}   ← 只有这三个能进 payload
  │     createIntent(it.intentItem, true, "", extra)
  ├─  批量容错：errs 是 map[string]string（key 为索引字符串），createdAny → wakeReview()
  │          ids 与输入 intents 等长同序，失败项 id=0；仅有失败时才返回 errors 字段
  │          返回 {"ids":[…],"proposed":true,"errors":{…}}
  └─  单条： wakeReview() → {"created":true,"proposed":true,"intent_id":N}

createIntent 内核里的「待审双条件」        agent/tools.go
  payload["review_status"] = "pending"
  payload["executor"]      = "review"
  ├─ 强制写入，顺序在 extraPayload 合并之后 → 外部无法覆盖
  └─  proposed ? AddProposedIntent : AddIntent
```

**为什么必须是唯一入口**：直投工具 `add_intent`（`agent/tools.go`）固定 `proposed=false`，
只会产生 `open` 意图；审核开启时 planner 若直接投递，意图会绕过审核面板被执行——
所以 `plannerSystem` 在审核开启时追加「【意图审核已开启（最高优先级约束）】…必须改用
add_review_intent」（`agent/planner.go`）。这只是提示词层约束：`add_intent` 仍在代码上
固定 `proposed=false`、直接创建 open，不能把审核开关描述成数据库强制拦截。

## 2.4 ReviewRunner：轮询、槽位与预审

### 2.4.1 生命周期（`server/review.go newReviewRunner` / ` Run`）

```
server/server.go  s.seedReviewIntercept()      启动期：补工具 + 种 ask 规则（见 2.6）
server/server.go  s.reviewRunner = newReviewRunner(s)
server/server.go  go s.reviewRunner.Run(s.ctx)

newReviewRunner
  interval: 15 * time.Second      ← 唯一轮询周期
  wake:     make(chan struct{}, 1)← 边沿信号（与 Task.notify 同构）
  slots:    map[string]*reviewSlot
  undecided: map[string]int       ← 「连续几轮全未决」计数器 /

Run
   sweep(ctx)                 ← 启动即扫一次，不等首个 tick
  for { select { ctx.Done / <-wake / <-ticker.C } → sweep }
```

### 2.4.2 preReviewTask 主流程（`server/review.go`）

```
  fresh := t.Store.FreshPendingIntents(50)     db/exploration.go（limit 50）
      └─ 空 → 直接 return（无待审即无事可做）
  if !rr.slotFor(t.ID).acquire("prescreen", 5*time.Minute)  → log 跳过本轮
      （人正在用面板/对话处置同一任务时，机器让路，见 2.8）
  versions := map[int64]int{ id: ContentVersion }            ← 全部当作 CAS 期望版本
  prov := rr.reviewProvider(t)  → nil 则 WARN「预审跳过 ⚠ reviewProvider() 为 nil」
  t.BeginLLMCall（defer EndLLMCall，占并发额度）
  cfg := agent.ReviewPreScreenConfig{Store, Goal, TaskID, Provider, FreshIDs, Emit}
  if t.ReviewAuto { rr.autoDecide(ctx, t, cfg, versions); return }   ← 自动处置分叉
  verdicts, err := agent.RunReviewPreScreen(ctx, cfg)
  逐条：note 超 200 rune 截断
    case "pass"   → SetPrescreenPassNote(id, note, versions[id])
          只写 pre_review='pass' 建议：state 保持 'proposed'、review_status='pending'，不 Notify、不放行（人工全审）
    case "revise" → SetPrescreenVerdict(id, note, versions[id])
  if passed+revised > 0 {
    log「预审落账（人工全审）：建议放行 %d / 建议修订 %d / 丢弃 %d」
    emit「预审完成：建议放行 %d 条、建议修订 %d 条（均待人工处置）」
```

**关键取舍**：
- 预审**只做机器能确定的事**：`pass` 只写建议（`pre_review='pass'`），`revise` 也只写建议
  （`pre_review='revise'`）——**既不驳回也不放行**，驳回/放行权永远留给人（或人工开启的自动处置）。
- 落库失败（err 或 ok=false）计 `dropped++` 并记 log
  「预审 pass 标注落库丢弃（版本冲突/已被处置）」：说明这条意图在读盘到落库之间被人处置过了，
  静默丢弃是正确的竞争处理。
- **零有效裁决**：当前实现没有专门的哨兵错误轮播分支 —— 若 LLM 没给出任何合法 id
  （或全部被 fresh 白名单过滤），循环体只是不执行：不落库、不 log、不 emit。
  下一轮 15s tick 时 `reviewed_version` 没被推进，这些意图仍是 fresh，会被自然重新取出，
  等价于「自动重试」；既不会卡死，也不会重复落账（重复处置由 `approveIntentAt` 的
  `state/executor/review_status` 校验兜住）。

### 2.4.3 autoDecide 自动处置（`server/review.go`）

```
  decisions := agent.RunReviewAuto(ctx, cfg)      tools_review.go
      │ 工具仍是 ReviewReadOnlyTools——决策以 JSON 返回，LLM 从不调写工具
  if len(decisions)==0 && len(cfg.FreshIDs)>0
        → rr.bumpUndecided(t.ID) >= 3 时 WARN「自动审核连续 3 轮以上全未决」
      else resetUndecided(t.ID)
  case "approve": t.Store.ApproveIntentAt(d.ID, versions[d.ID])
      │ !ok → 静默 continue（注释：CAS 冲突：人抢先处置了）
      │ emit「意图 #%d 已自动放行：%s」
  case "reject": _,_,ok,err := t.Store.RequestRevisionAt(d.ID, note, versions[d.ID])
      errors.Is(err, db.ErrNoteRequired) → bumpUndecided；>=3 轮 WARN「自动驳回连续缺理由」
      t.NotifyRevisionRequested(d.ID, note)   ← 驳回回流入口 ①
      │ emit「意图 #%d 已自动驳回（退回修订）：%s」
  if approved > 0 { t.Notify() }
  log「自动审核完成：放行 %d / 驳回 %d / 留人 %d」
```

### 2.4.4 reviewProvider 的 LLM 解析链（`server/review.go`）

| 顺序 | 位置 | 取值 | 失败后果 |
|---|---|---|---|
| ① | `server/review.go reviewProvider` | `GetAgentByKey("review")`，必须 `err==nil && a!=nil && a.Enabled` | 整个预审跳过（WARN「reviewProvider() 为 nil」） |
| ② | `server/review.go reviewProvider` | 遍历 `t.LLMProfileIDs` 逐个 `providerForProfile(pid)` | 继续下一条 |
| ③ | `server/review.go reviewProvider` | `a.LLMProfileID != nil` 时使用绑定档；为 nil 时才取 `ActiveProfile()` | 绑定档解析失败不会改试全局档，直接 nil；未绑定且全局档也不可用则跳过预审 |

即「agent 开关 → 任务配置 profile → agent 绑定档**或**（未绑定时）全局激活档」。③ 的两个取值是互斥分支，不是失败后的第四级回退；这条链与对话通道共用（`reviewChat` 复用同一函数）。

## 2.5 三通道工具矩阵（K14 / K15）

| 通道 | 装配点 | 工具集 | 能否写 db | 拦截形态 |
|---|---|---|---|---|
| ① 预审 `RunReviewPreScreen` | `agent/tools_review.go` | `ReviewReadOnlyTools()`（—） | 否（物理只读） | 无需拦截 |
| ② 自动 `RunReviewAuto` | `agent/tools_review.go` | `ReviewReadOnlyTools()` | 否（决策走 JSON，server 落库） | 无需拦截 |
| ③ 对话 `RunReviewChat` | `agent/tools_review.go` | `ReviewTools()`（—） | 是（5 个写工具） | 逐个走 K15 ask 卡 |

```go
// agent/tools_review.go   只读子集（K14）
ReviewReadOnlyTools = {GraphOverviewTool, ListFindingsTool, NodeDetailTool, listIntents}

// agent/tools_review.go   对话集 = 只读子集 + 5 个写工具（K15）
ReviewTools = ReviewReadOnlyTools + {setIntentStatus, sendPlannerHint,
              addReviewIntent, updateIntent, cancelIntent}

// intercept/intercept.go  写工具名单（guard 用它识别「审核写动作」）
ReviewWriteTools = ["set_intent_status","add_review_intent",
                    "send_planner_hint","update_intent","cancel_intent"]
```

**来源校验（K15 不误伤 planner 的唯一保险）**：`intercept.SkipReviewGateFor`
（`intercept/intercept.go`）先看工具是否在名单内，再看 ctx 里的 `agentName`：

```go
if !IsReviewWriteTool(toolName) { return false }
_, agentName := taskInfoFromCtx(ctx)
return agentName != "review"        // 非 review 来源 → 整段绕过拦截评估
```

所以同名工具在 planner/worker 里调用（送审、修订）**完全不受审核闸门影响**；
只有审核会话（`agent/tools_review.go` `intercept.WithTaskContext(ctx, TaskID, "review", Emit)`
打上 `agentName="review"`）才会弹卡。

## 2.6 ask 确认卡链（K15）

```
启动期种规则                                server/review.go seedReviewIntercept
  ①   GetEnabledTools() 取当前启用集，缺则 append 5 个审核写工具
            （默认启用集 intercept/intercept.go 只有 Bash/WebFetch/web_search 等 8 个，不含写工具）
  ②  逐工具种规则：[内置] 审核写动作确认：<tool>
            name/match_target="tool_name"/match_type="string"/pattern=<tool>
            action="ask"、priority 100、timeout_enabled=true、
            timeout_seconds=300、timeout_action="deny"

运行期判定                                  guard/guard.go applyIntercept
    interceptor==nil → 放行； SkipReviewGateFor(true) → 直接放行（来源非 review）
       不在启用集 → 直接放行（不受管控，与 7.6 一致）
       Match 规则命中？
   未命中 → Judge（LLM 兜底判官）
      deny  → block + Log "denied"
      allow → Log "allowed"
      ask   →  ctx.Err()? block「工作已取消，平台安全管控阻止执行」
                   HandleAsk → 返回 false → block「人工审批未通过（用户拒绝或审批超时）」

等待与决定                                  intercept/intercept.go HandleAsk
    CreateInterceptPending（conv_id 可为 NULL：后台任务也进审批页）
    ch := i.pending.add(pendingID)（先注册再复查已决状态，防 INSERT 后立刻决断丢信号）
   Activity{Kind:"intercept_request", Summary:"工具 %s 请求审批 (#%d)"}
   会话内联卡片（AppendConvActivity）或任务活动流（taskEmit）
       无超时 → 无限等； 超时 → ResolveIntercept("timeout", action,...)
  ▼
前端 POST /api/intercept/pending/{id}/decide
  server/server.go → server/intercept.go interceptDecide
  → Interceptor.Decide（intercept/intercept.go）→ 唤醒 ch → HandleAsk 返回 allowed
```

默认 300s 且超时动作是 `deny`（`intercept/intercept.go` + — 规则字段）——
审核写动作**宁可失败也不许静默放行**。

## 2.7 HTTP 五个端点（`server/server.go`）

| 方法 | 路径 | 处理器 |
|---|---|---|
| GET | `/api/tasks/{id}/review/proposed` | `server/review.go reviewListProposed` |
| POST | `/api/tasks/{id}/review/{intentID}/approve` | `server/review.go reviewApprove` |
| POST | `/api/tasks/{id}/review/{intentID}/reject` | `server/review.go reviewReject` |
| POST | `/api/tasks/{id}/review/chat` | `server/review.go reviewChat` |
| GET | `/api/tasks/{id}/review/messages` | `server/review.go reviewMessages` |

### 2.7.1 待审列表

```
  任务不存在 → 404 "task not found"
  !t.ReviewEnabled → 409 "该任务未开启意图审核（review_enabled=false）"
  t.Store.ProposedIntents(100)     db/exploration.go（ORDER BY priority DESC, id ASC）
  返回 {"intents":[{id,summary,priority,origin,pending_executor,
                       review_note,replaces,created_at,content_version}]}
```

### 2.7.2 approve / reject：409 语义与 expected_version 兼容路径

```
reviewApprove  / reviewReject  四道共有前门
  /  GetTaskById 失败 → 404
  /  !t.ReviewEnabled  → 409「该任务未开启意图审核（review_enabled=false）」
  /  ParseInt(intentID) 失败或 <=0 → 400 "invalid intent id"
  /  Decode body：approve {expected_version}；reject {reason, expected_version}
         reject 且 reason=="" → 400 "驳回理由必填"

版本兼容                                       server/review.go resolveReviewVersion
  if ev != nil { return *ev, nil }             ← 显式传 → 直接当期望版本
  n := es.GetNode(intentID); return n.ContentVersion, nil
  └─ 缺省路径取当前版本代入，使 db 侧 CAS 恒过（js 旧前端不必改）；
     读写间隙的竞争仍被 db 闸门 fail-safe 拒绝（不是「关掉 CAS」）

落库与 409 映射
  /  ver := resolveReviewVersion(...)
         t.Store.ApproveIntentAt(intentID, ver)
     errors.Is(err, db.ErrNotPendingReview|db.ErrStaleVersion|db.ErrIntentNotFound)
               → 409 "意图已被处理（并发冲突）"      其余 → 500
     !ok2 → 409（log「已不在待审池」）
         RequestRevisionAt(intentID, reason, ver)   （返回 summary,newVersion,ok）
     同三哨兵 → 409
         t.Notify()                    ← 放行后立刻唤醒 planner/worker
         t.NotifyRevisionRequested()   ← 驳回回流入口 ②
  /  emit「意图 #%d 已批准放行 / 已驳回：%s」   200 {"ok":true}
```

### 2.7.3 对话：两道单飞门（同任务同刻只跑一条）

```
  message 为空 → 400 "message 必填"
  if !t.reviewBusy.CompareAndSwap(false,true) → 409 "review 会话正在处理上一条消息"
      （进程内 atomic.Bool，server/manager.go；只防同一任务重复提交）
  chatSlot := s.reviewRunnerSlot(t.ID)              server/review.go
  !chatSlot.acquire("chat", 10*time.Minute) → reviewBusy 复位 + 409
        "审核轮正在处理该任务，请稍后重试"
      （与 runner 的 prescreen 槽位同一把锁 → 机器预审与人工对话互斥）
  emit Activity{Worker:"review", Summary:"【人工】"+msg}   ← 先把人话落进会话流
  go func {   defer slot.release / reviewBusy.Store(false)
     prov := (&ReviewRunner{s:s}).reviewProvider(t)   ← 复用 2.4.4 解析链
     t.BeginLLMCall
   agent.RunReviewChat(agent.ReviewChatConfig{
            Store, Goal, Provider, Message, Emit,
            Notify:     t.Notify,
            NotifyHint: t.NotifyHint,
            NotifyReview: func(int64){ s.wakeReviewRunner() },
            NotifyRevision: t.NotifyRevisionRequested,
            CheckExecutor: s.checkExecutorKey,
            Hooks:      t.Guard.Hooks() })                ← ask 卡钩子在这里接进 agent 循环
  错误 → emit Kind="error"「review 会话异常：」
  202 {"accepted":true}
```

`RunReviewChat`（`agent/tools_review.go`）自身装配：— 五个 Set*
（Notify/NotifyHint/NotifyReview/NotifyRevision/ExecutorChecker）→ ` ReviewTools()`
→ ` WithTaskContext(...,"review",...)` → ` renderSystem("review", reviewDefaultTmpl)`
→ ` captureRun(MaxTurns=30, MaxDuration=10min, PermissionMode=Bypass)`。

### 2.7.4 消息页

```
  若 review_enabled=false → 409；否则读取 review 活动：
  t.Store.ActivityPage(db.ActivitySessionFilter{Worker:"review"}, 0, 200)
  仅保留 kind=text/result/error；人工发言（【人工】前缀）改写为 kind=user
  返回 {"messages":[{id,kind,summary,is_error,created_at}]}
  （intercept_request 等活动被过滤；审批卡不使用此返回体）
```

## 2.8 三层互斥与 CAS 落点

| 层 | 机制 | 位置 | 作用域 |
|---|---|---|---|
| 进程内 | `reviewSlot` 租约 `acquire(owner, ttl)`：`busy && now.Before(until)` 拒绝，过期则 WARN 抢占 | `server/review.go` | 同任务：prescreen 5min / chat 10min |
| 进程内 2 | `Task.reviewBusy`（`atomic.Bool` CAS） | `server/review.go` | 同任务对话消息单飞 |
| DB 行锁 | `SELECT … FOR UPDATE` | `db/exploration.go`（批准）/ —（驳回）/ `db/intent_edit.go`（改稿） | 同一意图行 |
| DB CAS | `content_version` 比对 + `WHERE … AND content_version=$4` | `db/exploration.go`、`db/intent_edit.go` | 读到写之间的竞争 |

租约的 owner 名（` ownerName`）只用于日志与抢占提示，**正确性靠 DB 行锁 + CAS**；
租约的职责是「别让机器和人在同一任务上互相踩」的体验保护（省 LLM 钱、防并发刷屏）。

## 2.9 驳回回流（revision_requested）三个入口与修订链

```
入口 ① 自动审核   server/review.go  t.NotifyRevisionRequested(d.ID, note)
入口 ② 人工面板   server/review.go  t.NotifyRevisionRequested(intentID, reason)
入口 ③ 审核对话   agent/tools_review.go  t.notifyRevision(id, reason)
                    （set_intent_status action=reject 分支）

统一入队                                 server/manager.go NotifyRevisionRequested
   守卫：intentID<=0 || reason=="" → 整条丢弃（不失真成空事件）
   pendingTriggers += TriggerEvent{Kind:"revision_requested", IntentID, Detail: reason}
        t.Notify()                    ← 边沿信号（cap 1）

planner 侧承接
  agent/planner.go   renderTriggers 文案：请用 update_intent 改这条意图本身
  agent/tools.go     graphOverview.rejected_intents  ← 事件丢失（进程重启）时的持久兜底
  server/engine.go   抑制分支 drainTriggersRetaining("revision_requested","hint")
  server/engine.go   有 rejected → 跳过 goalless，放行 planner 轮
  server/engine.go   结果侧 HasRejectedIntent 守卫：有驳回稿则拒绝落 done
```

修订链（planner 用 `update_intent`，`agent/tools_intent.go`）：

```
  validate：intent_id>0；expected_version 必填（提示先 list_intents 取 content_version）
  四个可改字段全空 → 「至少要传一个待修改字段」
     mainAgentOpenOnly（仅 t.worker=="human" 生效；planner 不受限）
  target_executor 非空 → checkExecutor
 asset_ids 非空 → as.CheckAssetsIntercept(taskID, ids)，命中即报「授权的测试范围」错误
    t.ts.UpdateIntent(id, *ExpectedVersion, db.IntentPatch{...}, t.worker)
        │ db/intent_edit.go
        │   FOR UPDATE； 状态门禁（pending 稿：「正在审核中（pending），等审核结论或先驳回」）
        │   版本 CAS； 产出锁（已有 yields/spawns → ErrIntentProduced）
        │   open 意图 + target_executor → 「open 意图无审核链，target_executor 无意义」
        │   驳回稿但无实改 → ErrNoChange「驳回稿必须修改内容后才能重送审核」
        │   驳回稿有实改 → review_status="pending"、删 review_note、reviewReset=true
        │   UPDATE payload/priority/content_version+1；锚点表整组替换
        └ 返回 IntentUpdate{ReviewReset}
reviewErrText 将 ErrStaleVersion → 「版本冲突…重新 list_intents」；其余哨兵原样透出
 if upd.ReviewReset && t.notifyReview != nil { t.notifyReview(upd.ID) }
        └ 立刻唤醒审核轮（不等 15s tick）—— 重送稿马上进预审
```

取消链（`cancel_intent`，`agent/tools_intent.go`）：`reason` 必填（—，
记 `delete_reason` 供审计）→ `SoftDeleteIntentAt`（`db/intent_edit.go`）→
`notifyHint`（—）把「意图已取消」送进下一轮 planner；
待审/驳回稿在 `mainAgentOpenOnly` 下不可取消，必须走审核面板。

---

# 第 3 章 · planner 轮详解

> 第 1 章 1.3.2/1.4 已给出门禁链与 `Plan` 的骨架行号；本章讲「一轮规划到底读了什么、
> 说了什么、能做什么、如何收场」，以及它与审核域的耦合点。

## 3.0 一轮规划的完整时序

```
① 唤醒源（任一个）                        server/manager.go
   worker 完成 NotifyDone  │ finding  │ 人工 goal  / hint
   goal_deleted  / goal_edited  │ cancelled  │ revision_requested
   │  事件入 pendingTriggers，notify chan（cap 1）只做边沿
   ▼
② plannerLoop 两臂                        server/engine.go
   notify 臂  runRound("edge") │ 心跳臂 （interval ≥ 600s）
   ▼ runRound
   debounce （突发合并成一次）
   ▼ 门禁链 （见 1.3.2；本章 3.2 补三条）
   ▼
③ 取走事件本体  t.drainTriggers()      （ 取走并置 nil）
   ▼
④ Plan                                   agent/planner.go
    situational := renderTriggers(triggers) + renderGraphOverview(...)
    plannerSystem(...)（四级）→  constraintBlock →  deferredSystem
    input := lead + situational + 判目标指令   ← 情境放 user 消息（3.3.3）
    captureRun（LLM + 工具循环）
   ▼
⑤ 工具循环里产出：add_intent / add_review_intent / update_intent /
   prove_goal / goal_met（3.6）；退出时 tsx.GoalMet 即收官结论
   ▼
⑥ Plan 返回  → engine 分流 （met 需过  rejected 守卫）
```

## 3.1 唤醒事件的生产与消费

`TriggerEvent` 结构见 1.3.2（`agent/planner.go`，8 个字段）。各 Kind 的语义与消费点：

| Kind | 生产者 | 载荷字段 | planner 侧落点 |
|---|---|---|---|
| `done` | `NotifyDone ` | `IntentID` | `renderTriggers `（default 分支）+ `factIDsYielded ` |
| `finding` | `NotifyFinding ` | `IntentID` + `Detail` | — |
| `goal` | `NotifyGoal ` | `Goals[]` | — |
| `hint` | `NotifyHint ` | `Hints[]` | —（**Kind 就是 "hint"**，不是 "planner_hint"） |
| `revision_requested` | `NotifyRevisionRequested ` | `IntentID` + `Detail`(理由) | — |
| `goal_deleted` | `NotifyGoalDeleted ` | `Detail` | — |
| `goal_edited` | `NotifyGoalEdited ` | `OldGoal`/`NewGoal` | — |
| `cancelled` | `NotifyCancelled ` | `Summary`（删除前捕获） | — |

排空语义（`server/manager.go` / —）：

```
drainTriggers()                  取走整批并置 nil → 本轮 Plan 看到的就是「上次消费以来的全部变动」
drainTriggersRetaining(kinds)    逐事件比对 Kind，命中的留在队列，其余丢弃
      当前唯一调用点 server/engine.go，白名单 ("revision_requested","hint")
      ├ 理由：done/finding 是可再生信息（graph_overview 里还有），丢了不影响决策
      └ 而「人给的提示」与「驳回理由」是一次性指令，丢了 planner 就永远不知道
```

**不变量**：一次唤醒 ≠ 一次事件。`notify` 只是边沿（cap 1，重复 Notify 会被丢弃），
事件本体在 `pendingTriggers`；因此**任何 Notify 都必须同时携带完整载荷**，
否则事件合并后信息就只剩最后一条。

## 3.2 门禁的三条补充说明（对照 `server/engine.go plannerLoop`，逐行行号见 §7.4）

```
A. 待审抑制
   HasProposedIntent()（db/exploration.go）为真 → 本轮规划没有意义：
   待审意图不能派单、不能推进，planner 再想也只是空转烧钱 → 直接 return。
   但 drainTriggersRetaining 保留 revision_requested/hint，让抑制解除后一次送达。

B. goalless 豁免
   抑制之前先判一次「是不是纯粹在等审核」：目标全 met/无 open 目标 + 只有待审
   → log「goalless 收尾豁免」+ return（保持 running，等人工处置）。
   避免「任务因一条待审意图被永久卡在 running 无法收尾」。

C. rejected 例外
   goalless 判定要排除「有驳回稿」的情形：否则驳回稿永远等不到修订轮
   （修不了 → 收尾 → 死锁）。所以 hasRejectedIntent 为真时跳过 goalless 直接进 Plan。
   结果侧还有第二道： met && HasRejectedIntent → 拒绝落 done（防「边驳回边收官」）。
```

心跳抑制（—）：`HasActiveIntent()==false && HasProposedIntent()==true` 时
log「planner 心跳抑制：仅待审意图无活跃意图」并 `continue`——心跳是为推进服务的，
纯等审核时不该产生 LLM 调用。

## 3.3 情境注入：renderTriggers + graphOverviewData

### 3.3.1 renderTriggers 文案（`agent/planner.go`）

| Kind | 位置 | 文案要点 |
|---|---|---|
| `goal` / `hint` / `goal_deleted` / `goal_edited` | — | 人工增删改目标 / 战略提示 |
| `finding` | — | 「意图 #%d 的 worker 报告了 finding」 |
| `cancelled` | — | 摘要优先取 `ev.Summary`（节点已软删，回查不到） |
| `revision_requested` | — | 「意图 #%d 被审核驳回，理由：%s。请用 update_intent 修改这条意图本身（不要新建替代意图，不要丢掉它的资产与血缘），改完会自动重新送审。」 |
| `done`（default） | — | worker 结束结论 + `factIDsYielded`（—） |

尾注 —：「（完整细节可 node_detail / get_worker_output / list_findings 再查。）」——
触发器只给摘要，细节让 agent 自己决定要不要拉，避免情境块无限膨胀。

### 3.3.2 graphOverviewData 键表（`agent/tools.go`）

| 键 | 位置 | 内容/上限 | 用途 |
|---|---|---|---|
| `goals` | — | 目标文本 + 状态 | 判断还剩什么 |
| `traffic_refs` | — | 流量绑定（开关控制） | vuln 场景定位报文 |
| `hints` | — | 人工提示 | 战略约束 |
| `parentsOf` / `yieldsOf` | — / — | derived_from / yields 边 | 血缘与产出 |
| `open_intents` | — | `openIntentsCap=30`（—） | 已有待执行队列 |
| `running_intents` | — | 在跑 | 防重复规划 |
| `proposed_intents` | — | `ProposedIntents(20)` | 待审可见，但**不可派单** |
| `rejected_intents` | — | `RejectedIntents(20)` | 驳回待修订的持久兜底 |
| `done_intents_total` | — | 计数 | 进度感 |
| `frontier_open` | — | `len(fr)`（— 兜底） | 队列压力 |
| `findings_total` / `facts` | — / — | 计数 + 事实摘要 | 已有认知 |
| `finding_list` | — | cap 10 | 近期发现 |
| `recent_facts`（`recentFactsCap=20`） | — | 近期事实 | 冷热分层 |
| `recent_done_intents` | — | 近期完成 | 避免重复 |
| `cold_digests` / `cold_digests_more` | `agent/tools.go graphOverview` | 压缩摘要及 expand_digest 的可展开列表 | 长任务记忆 |

`rejected_intents` 是**双保险的另一半**：`revision_requested` 事件只在内存队列里，
进程重启就丢了；但驳回态的意图行一直在库里，planner 只要看概览就能发现「我还有稿要改」。

### 3.3.3 为什么情境放 user 消息

`agent/planner.go` 组装 situational，但真正拼进提示词的是 —
`input := lead + situational + 判目标指令`——即情境进**用户消息**，system 只放
`plannerSystem` 四级的稳定正文。原因：system 前缀越稳定，上游 KV/前缀缓存命中率越高；
每轮都会变的触发器与态势若进 system，等于每轮都让整段 system 失效。

## 3.4 提示词四级链（`agent/planner.go plannerSystem`）

```
①  任务级快照 tasks.planner_prompt_override（建任务时定格，见 1.1）
        注入点 agent/planner.go SetTaskPromptOverride ← server/task_llm.go
②  PromptOverride("planner")（DB agent_prompts 当前正文，agent/prompt.go renderSystem）
③  PlannerDefaultTemplateBodyFn()（DB planner_templates is_default 行）
        种子：server/assembly.go SeedPlannerTemplateIfEmpty（首插 only）
④  代码内置 plannerDefaultTmpl（永不空）

每级 renderTmpl(tmpl, PlannerVars{Goal,Scope,AssetSummary,DataDir,Now})（agent/prompt.go）
**planner 运行期只填 Goal/DataDir/Now；Scope/AssetSummary 为空串（不报错），模板预览才填完整字段。**
渲染失败（missingkey=error 等）→ 记 log 并回退下一级，最终必有正文
```

### 3.4.1 审核开启时的强制约束块（`agent/planner.go`）

```
if reviewEnabled → sysBody += "【意图审核已开启（最高优先级约束）】…新增意图必须改用
                             add_review_intent（add_intent 产出的意图会绕过审核）"
```

它与 `agent/tools_review.go` 的运行时校验、`agent/tools.go` 的强制 payload
共同构成「提示词 → 工具 → 数据」三道防线（详见 2.3）。

### 3.4.2 开场白三分流（`agent/planner.go`）

```
有 trigger            → 「刚有具体变动」（要求先看变动再决策）
无 trigger（心跳）     → 「定时巡检」（自己判断要不要动）
无活跃意图（空转）     → 「必须产出 ≥1 意图」硬底线（防 planner 空谈不干活）
```

## 3.5 工具集装配（`agent/planner.go`）

```
  tsx := NewToolSet(ts, "planner")          ← worker 名 = "planner"（来源标识）
 FindingRecorder / AssetStore / TaskID / CoverageEnabled / killWork / steerWork
  SetNotifyReview      ← server/task_llm.go（→ s.wakeReviewRunner）
 SetReviewEnabled(闭包) ← server/task_llm.go（→ t.ReviewEnabled）
  SetExecutorChecker   ← server/task_llm.go（→ s.checkExecutorKey）
 SetOwnerNode(origin fact)  ← 新资产/事实默认挂 origin，避免孤儿
  base := DropCoverageTools(PlannerTools()) + actool.DefaultTools()
 WithRunInfo → AugmentTools(ctx,"planner",base)   ← 技能/MCP/自定义工具（第 6 章）
```

`PlannerTools`（`agent/tools.go`）是大全表；`DropCoverageTools` 按任务设置剔除覆盖率工具，
`AugmentTools` 再按 skill/MCP 配置补齐——最终工具集是「静态度 + 动态增强」两段拼装。
注意 `Plan` 里**没有** `SetNotify / SetNotifyHint`：planner 不自唤醒，下一轮只从 engine 侧来。

## 3.6 意图产出的四条出口

| 出口 | 位置 | 产物 | 何时用 |
|---|---|---|---|
| `add_intent` | `agent/tools.go` | `state='open'` 直投 | 任何任务都可直接创建 open；审核任务应改用 `add_review_intent`，审核开关不是物理封锁 |
| `add_review_intent` | `agent/tools_review.go` | `state='proposed'` + `executor='review'` | 任务开审核（唯一合法送审口） |
| `update_intent` | `agent/tools_intent.go` | 改稿 → 驳回稿自动重送 | 收到 `revision_requested` |
| `prove_goal` / `goal_met` | `agent/tools.go` / — | goal → `met`，置 `GoalMet` | 证成目标 / 提前收官 |

公共内核 `createIntent`（`agent/tools.go`）的五道门，顺序即语义：

```
  summary 非空
 parent_ids 必须是本任务的 fact/finding（否则血缘指向空气）
 priority 缺省 5、上限 10 夹逼
 asset_ids → CheckAssetsIntercept（命中即拒，越界不落库）
 extraPayload 白名单合并（pending_executor/replaces/review_reason）
 proposed 强制双条件（防覆盖）
 AddProposedIntent / AddIntent（单事务：节点 + 锚点）
 血缘：parent derived_from；无 parent 挂 origin fact（Link 失败必 log）
```

## 3.7 goal_met 判定与收官

```
prove_goal（agent/tools.go）
   校验 goal 是 goal 节点、evidence 是 fact/finding
   Link(ev, proves, goal) +  SetNodeState(goal,"met")
   全目标扫描 allMet →  t.GoalMet=true   ← 不显式调 goal_met 也能收官

goal_met（agent/tools.go）
   t.GoalMet = true    无 allMet 校验，全局收官工具（模型自主判断）

主线
  Plan  return tsx.GoalMet → engine  →  met
    →  HasRejectedIntent() 为真 → 只 log「目标达成判定被拦截：仍有被驳回待修订意图」
    →  SetTaskStatus(t.ID,"done") →  cancelExec(t.ID, agent.AbortGoalMet)
    → 在跑 worker 的 ectx 被取消 → 1.3.4  落 stopped

旁路 A goalless（engine.go）：纯 Go 收尾，不经 LLM（见 3.2-B）
旁路 B 超时收尾（engine_timeout.go settleTask）：终局 Planner 轮  → status=done/timeout
```

## 3.8 一轮规划的自查清单

1. 有 `revision_requested` 或驳回态意图？→ 本轮必须先改稿，不许新建替代意图（3.1/2.9）。
2. 有待审意图在池？→ 除改稿外的规划都无意义（3.2-A）。
3. 目标全 met 且无驳回稿？→ 走 goalless 或 `goal_met` 收尾，别继续派单（3.2-C / 3.7）。
4. 开审核的任务？→ 一律 `add_review_intent`，`executor` 由审核通道决定（2.3）。
5. 情境块只在 user 消息里，system 保持稳定（3.3.3）。

---

# 第 4 章 · 种子意图与自定义 agent 执行链

### 4.0 全景

```
配置面：agents 表给自定义 agent 声明 executor_key（4.1）
             │
入口 A  HTTP POST /api/tasks {pre_agent, seed_first_intent:true}   （4.2）
入口 B  LLM 调 spawn_task{pre_agent, seed_first_intent}            （4.2）
             │ 两处同款三层校验：查得到 ∧ Enabled ∧ !Builtin
             ▼
launchTask → seedFirstIntent（server/server.go）
             │ payload["executor"] = t.PreAgent      ←  唯一写入点
             │ AddIntent(payload, 8, nil, "seed")    ←  永远 open
             ▼
engine claimNext 领取（state=open → running）
             │
runIntent（server/engine.go）
             │  解 payload.executor ──唯一路由读取点──┐
             ▼                                                  │
executorKey != "" 且非消息续跑？                                  │
   是 ──▶ resolveExecutor 三层校验（engine.go）              │
           ├─ 失败 → R6 回退默认 worker（不失败）     │
           └─ 成功 → RunCustomExecutor（agent/customexec.go） │
   否 ───────────────────────────▶ 默认 worker.Execute
```

### 4.1 配置面：executor_key 怎么来的

**agents 表 DDL**（`db/schema.sql` + —）：

```sql
CREATE TABLE IF NOT EXISTS agents (
    id BIGSERIAL PRIMARY KEY,
    key  TEXT NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_]*$'),   --
    name/description/role TEXT,          -- role 硬编码 'assistant'
    builtin BOOLEAN NOT NULL DEFAULT true,   -- 自定义 agent = false
    enabled BOOLEAN NOT NULL DEFAULT true,
    llm_profile_id BIGINT,               -- 执行器 LLM 绑定（nil→全局激活配置）
    current_prompt_id BIGINT, max_turns INT DEFAULT 0,   -- 0=不限
    run_seconds INT DEFAULT 1200,
    web_search BOOLEAN DEFAULT false,
    trigger_run_mode  TEXT DEFAULT 'serial'  CHECK (… IN ('serial','parallel')),  --
    trigger_merge_mode TEXT DEFAULT 'all'   CHECK (… IN ('by_task','all','none')), --
    trigger_max_parallel INT DEFAULT 5       --
);
-- executor_key 列（P-EXEC·D10）：
ALTER TABLE agents ADD COLUMN IF NOT EXISTS executor_key TEXT;
ALTER TABLE agents ADD CONSTRAINT agents_executor_key_ck
    CHECK (executor_key IS NULL OR executor_key ~ '^[a-z][a-z0-9_]*$');
CREATE UNIQUE INDEX idx_agents_executor_key ON agents(executor_key)
    WHERE executor_key IS NOT NULL;
```

声明路径：

```
HTTP PATCH /api/agents/{key}  {executor_key: "vuln_dig"}
  server/server_mgmt.go
     内置 agent → 400「内置 agent 不可声明 executor_key（K18：仅自定义 agent）」
    格式：len>64 或不符合 reAgentKey → 400
    唯一性预检 → 409
     pg.SetAgentExecutorKey(agentKey, executorKey)
            └ db/config.go：正则 executorKeyRe + UPDATE … WHERE key=$1 AND builtin=false
              RowsAffected==0 → 「agent %q 不存在或是内置 agent」
```

**注意**：`GetAgentByExecutorKey`（`db/config.go`）SQL 不带 enabled/builtin 条件（`SELECT … FROM agents WHERE executor_key=$1`），三层过滤全部由调用方完成——这正好引出 4.2 的两道入口校验。

### 4.2 两个建任务入口的 pre_agent 校验（同款三层）

```
入口 A：HTTP createTask
  server/server.go  请求字段 PreAgent string `json:"pre_agent,omitempty"`
                         注释「种子意图预选执行器 executor_key（仅 seed_first_intent=true 时有意义）」
  server/server.go
      if req.PreAgent != "" {
          ea, eaErr := s.m.PG().GetAgentByExecutorKey(req.PreAgent)
          if eaErr != nil || ea == nil || !ea.Enabled || ea.Builtin {
              → 400「前置执行器 %q 不可用（须为已启用自定义 agent 的 executor_key）」
          }
      }
  server/server.go  落 Task 行：PreAgent: req.PreAgent
  server/server.go  launchTask(t, desc+" "+goal, *req.SeedFirstIntent)

分类（场景）继承（P-SCENARIO·MERGE，`server/server.go`）：
  建任务若带 task_category（或其默认场景）→ ReviewEnabled / SeedFirstIntent / PreAgent /
  Description 四项**未显式传值时由 task_categories 的分类默认值兜底**；
  显式传值优先，缺省才继承——所以「场景开了审核」会静默把新任务拉进审核流。

入口 B：LLM 工具 spawn_task
  server/orchestration.go  同款三层，失败 → actool.Errorf
      「pre_agent 无效：…；可用：%s」（可用清单 executorKeyList() ：
      遍历 ListAgents() 过滤 ExecutorKey!="" ∧ Enabled ∧ !Builtin；空则提示先声明）
  server/orchestration.go  PreAgent 一次性透传（不落 tasks 列）
  server/orchestration.go  review_enabled 继承：父开→子开；父关→按所选 planner 模板 nkey
             反查 task_categories.default_review_enabled 兜底；无模板则纯继承；
             dispatcher 不能通过参数覆盖此规则
  server/orchestration.go  launchTask(t, …, a.SeedFirstIntent)
```

### 4.3 seedFirstIntent：种子意图写入

```
server/goals.go launchTask
    if seedFirstIntent → s.seedFirstIntent(t)
    s.admitTask(t, "bootstrap")

server/server.go seedFirstIntent
   注释即结论：「种子意图永远 open 落库（seed_review 已废除，种子不送审）；
              pre_agent 非空时 payload.executor=executor_key 直发自定义 agent；
              不设 pre_agent 走现状 AddIntent 路径零变化（K23）」
    summary := "完成任务目标：%s（任务：%s）"
    payload := {"summary": summary}
    if t.PreAgent != "" { payload["executor"] = t.PreAgent }   ←★唯一写入点
    id := t.Store.AddIntent(payload, 8, nil, "seed")
           └ db/exploration.go AddIntent → AddNode("intent", …, "open", …)
             state 硬编码 "open"，priority=8，origin="seed"
    Link(originFact, derived_from, id)   ← 血缘挂根
    log「[seed] task %s: 已下发种子意图 #%d（预选执行器 %q，池领取后直接路由执行）」
```

### 4.4 engine 领取后的 executor 路由

```
server/engine.go claimNext(t, name)
    Frontier(20) 逐个尝试 ClaimIntent（open→running 的 CAS 见第 7 章）
         由 runWorkerStep（ 起）在 workerLoop 里调用

server/engine.go runIntent(ctx, t, name, worker, intent, requestID, message)
    hasChatMessage := message != ""

  ★payload.executor 的唯一路由读取点：
      executorKey := ""
      var p map[string]any
      if json.Unmarshal(intent.Payload, &p) == nil {
          if ex, ok := p["executor"].(string); ok { executorKey = ex }
      }
  （全仓其它 payload["executor"] 出现处均非路由读：server.go 种子写、
    tools.go 审核占位写 "review"、tools.go 展示层过滤、
    exploration.go 批准转正、intent_edit.go 写 pending_executor）

    if executorKey != "" && !hasChatMessage {
         ↑ 分流条件：人发消息续跑（hasChatMessage）强制走默认 worker
           （ DEBUG「executor=%s 但属消息续跑，走默认 worker」）
           │
           ▼
         resolveExecutor 三层校验（engine.go）：
             key=="" → "executor_key 为空"
             GetAgentByExecutorKey 报错 → "查询执行器失败"
             a==nil → "执行器标识 %q 未注册"
             !Enabled → "执行器 %q 所属 agent 已禁用"
             Builtin → "内置 agent 不能作为自定义执行器"
           │
           ├─ 失败（R6 回退）：
           │    emitActivity「意图 #%d 指定执行器 %q 不可用（%v），已回退默认 worker」
           │    + WARN log ── 不失败，继续默认 worker
           │
           └─ 成功：
                prov := e.providerFn(llmProfileOrZero(ea.LLMProfileID))  ←
                reason, wrote, err = agent.RunCustomExecutor(workCtx,
                    CustomExecutorConfig{Agent: ea, Provider: prov, TaskID, Intent,
                        Assets, Store, Goal, Hooks, Emit, Enrich,
                        NotifyFinding: t.NotifyFinding,
                        WorkDir, ProxyAddr, ProxyCACert})
                routed = true
         }
    if !routed → worker.ExecuteWithMessage / worker.Execute（默认路径）
    model_error 重试段只重试 worker.Execute*，不重试 RunCustomExecutor
```

### 4.5 RunCustomExecutor 执行体（`agent/customexec.go`）

```
  RunCustomExecutor(ctx, cfg CustomExecutorConfig) (TerminalReason, WriteCounts, error)
  name := "exec:" + cfg.Agent.Key          ← 活动流 worker 名，可追溯执行器
  工具底座：
        tsx := NewToolSet(cfg.Store, name); SetTaskID; SetCoverageEnabled;
        SetOwnerNode(intent.ID); SetEnrich; SetNotifyFinding
        base := WorkerTools() + defaultToolsExcept("MultiEdit","Glob","Grep")
        tools, def, cleanup := AugmentTools(ctx, cfg.Agent.Key, base)   ← 第 6 章装配
  runDir := ensureRunDir(WorkDir, TaskID, Intent.ID)
        （<workDir>/tasks/<taskID>/i<intentID>，agent/worker.go）
        intercept.WithReviewContext(ctx, runDir, ReviewBackground{})
  log「[executor] 自定义执行器启动：task %d 意图 #%d agent=%s」
  系统提示词：
        sysBody := renderSystem(cfg.Agent.Key, "", {Goal, DataDir, Now})
                   └ 第 6 章：DB 无模板时 def="" 起点为空
        sysBody += RenderIntentTask(cfg.Intent)
                   └ agent/worker.go「【你领到的意图（本次唯一任务：
                     只做这一条、只产生事实、做完即停）】：…意图 id: %d」
        sysBody += "开始执行上面这条意图：只做它、只产生事实、assets、finding、做完即停。"
  agentcore.Options：SystemPrompt / DynamicBoundary / Tools / DeferredTools /
        UnlockSet / PermissionMode: Bypass / EnableWebFetch+代理 / BashEnv /
        WorkingDir: runDir /
        MaxTurns: cfg.Agent.MaxTurns（0=不限） /
        MaxDuration: time.Duration(cfg.Agent.RunSecs)*time.Second /
        Settlement: wrapupSettlement(key, ["Bash"]) / ToolOutputDir / Todos
        （Hooks 仅非空才挂）
 emitWrap：a.NodeID, a.Worker = &intentID, name
 input := "执行你领到的意图。" → captureRun(ctx, opts, input, emitWrap)
 零写入兜底：Writes().Total()==0 → AddNode("fact",
          {"text": "意图 #%d 已由执行器 %s 执行（无结构化写回）"}, 0, "confirmed", name, [intentID])
 完成日志 + 返回 (reason, tsx.Writes(), nil)
```

**与 `worker.Execute` 底座（`agent/worker.go`）的差异对照**（customexec.go 内 `rg` 验证无这些符号）：

| 项 | 默认 worker 有 | 自定义执行器 |
|---|---|---|
| FindingRecorder | ✓ | ✗（但保留 SetNotifyFinding） |
| renderWorkerGraphOverview 态势概览 | ✓ | ✗（看不到任务全局图） |
| constraintBlock 操作约束 | ✓ | ✗ |
| EnableWebSearch | ✓（按配置） | ✗ |
| Compaction 压缩配置 | ✓ | ✗（无会话态） |
| Transcript/SessionID/Resume | ✓ | ✗（与 engine.go 注释一致：对话上下文仅在 worker 会话） |
| 意图锚定资产块 / taskclock clampMaxDuration | ✓ | ✗ |
| WorkerTools()+defaultToolsExcept、AugmentTools、ensureRunDir、WithReviewContext、PermissionMode Bypass、proxyEnv/cmdOutDir、wrapupSettlement、Todos、emitWrap 打 NodeID/Worker、零写入占位 fact | ✓ | ✓（完全相同） |

### 4.6 提示词链：DB 是可编辑权威源

```
agent/prompt.go renderSystem(agentKey, def, vars)
  if PromptOverride != nil {
      if t, ok := PromptOverride(agentKey); ok && t != "" { tmpl = t }
  }
  → template.Option("missingkey=error") 渲染；失败回退 def（再失败回退原始字符串）
  「an agent never starts with a half-rendered prompt」

注入实现 server/server.go：
  agent.PromptOverride = func(key) (string, bool) {
      a := pg.GetAgentByKey(key);  t := pg.CurrentPrompt(a.ID)   -- db/config.go
      return t, t != ""
  }

播种 db/config.go SeedPromptIfEmpty：仅 current_prompt_id IS NULL 时
      SavePrompt(agentID, 内置默认模板, "内置默认", "system")
自定义 agent 无 per-key 代码默认：
  chat 路径 fallback DefaultAssistantPrompt（agent/chat.go）
  执行器路径 fallback 空串（agent/customexec.go）── DB 无模板 = 只有意图块
```

### 4.7 第 4 章方法速查

| 方法 | 位置 | 简介 |
|---|---|---|
| `GetAgentByExecutorKey` | `db/config.go` | 按 executor_key 查 agent（不带 enabled/builtin 条件） |
| `SetAgentExecutorKey` | `db/config.go` | 声明/清除 executor_key（仅非内置） |
| `createTask` pre_agent 校验 | `server/server.go` | HTTP 入口三层校验 |
| `toolSpawnTask` pre_agent 校验 | `server/orchestration.go` | spawn_task 入口三层校验 |
| `seedFirstIntent` | `server/server.go` | 种子意图写入；永远 open |
| `Engine.claimNext` | `server/engine.go` | Frontier 领取（open→running） |
| `Engine.runIntent` | `server/engine.go` | 执行分发：executor 路由 + 默认 worker |
| `Engine.resolveExecutor` | `server/engine.go` | 路由三层校验（空/未注册/禁用/内置） |
| `agent.RunCustomExecutor` | `agent/customexec.go` | 自定义执行器执行体 |
| `renderSystem` / `PromptOverride` | `agent/prompt.go` / `server/server.go` | 提示词 DB 覆盖链 |

---

# 第 5 章 · finding 触发自定义 agent 的完整链路

### 5.0 全景

```
worker 调 report_finding 落库（agent/tools.go）
  ├─ 返回文本首行 "finding recorded: <nodeID>" + JSON
  └─ t.notifyFinding / t.notify() 唤醒 planner（第 1 章）── 这是「任务内回流」

另一条「跨会话触发」链（本章主题）：
Scheduler 5s tick（server/scheduler.go）
  step()→ fireIntervals / fireFindings / fireGoals /
                       fireTaskTimeouts / fireToolCalls / fireTaskCreates
      │  读 activity 表（kind='tool_result'）按水位增量取事件
      ▼
StartTriggeredRun（server/conversations.go）
      │  入 triggerQ[agentKey]；按 trigger_run_mode/merge_mode 合并（5.3）
      ▼
pumpLocked按并发上限起 goroutine
      ▼
runTriggeredRun：新建 conversation + 追加触发消息
      ▼
runConversationTurn（+）→ ChatAgent.Chat（agent/chat.go）
      │  DefaultTools + AugmentTools(agentKey)（第 6 章装配）
      │  RunInfo.TaskID=0（无任务运行时上下文，任务上下文只在消息文本里）
      ▼
工具写回：update_finding_report / get_task_graph(显式 task id) / …
      → pg.AppendConvActivity（conversations 活动表，独立于 tasks activity）

★关键澄清（5.7）：你的部署里 "vuln_* finding 触发" 实际是自定义 agent 配了
  on_tool_call + tool_names=["report_finding"] 的触发器（与内置 reporter 同款），
  或 on_finding=true；两种都在 scheduler.go，vulnclass 本身没有任何代码级派发。
```

### 5.1 触发器数据模型：`agent_triggers` 表

DDL（`db/schema.sql`）：

```sql
CREATE TABLE IF NOT EXISTS agent_triggers (
    id BIGSERIAL PRIMARY KEY,
    agent_key        TEXT NOT NULL,              -- 无外键；仅自定义 agent
    enabled          BOOLEAN NOT NULL DEFAULT true,
    interval_sec     INTEGER NOT NULL DEFAULT 0,
    on_finding       BOOLEAN NOT NULL DEFAULT false,     -- finding 落库触发
    on_goal_met      BOOLEAN NOT NULL DEFAULT false,
    on_task_timeout  BOOLEAN NOT NULL DEFAULT false,
    on_tool_call     BOOLEAN NOT NULL DEFAULT false,     -- ★工具调用触发
    on_task_create   BOOLEAN NOT NULL DEFAULT false,
    interval_message / finding_message / goal_message /
    task_timeout_message / tool_call_message / task_create_message TEXT DEFAULT '',
    tool_names       TEXT NOT NULL DEFAULT '',   -- JSON 文本（非数组列）
    last_fire / created_at / updated_at TIMESTAMPTZ
);
CREATE INDEX idx_agent_triggers_agent ON agent_triggers(agent_key);
```

Go 侧：`db.AgentTrigger`（`db/triggers.go`，字段一一对应；`ToolNames []string` 存 JSON 文本）；CRUD：

| 方法 | 位置 | 简介 |
|---|---|---|
| `CreateTrigger` | `db/triggers.go` | INSERT 15 列 RETURNING |
| `UpdateTrigger` | — | 按 id 更新，不碰 last_fire |
| `ListEnabledTriggers` | — | `WHERE enabled ORDER BY id` |
| `ListTriggersFor` | 同文件 | 按 agent_key 列 |
| `NewToolCallsSince` | — | 工具事件源 SQL（见 5.2） |

### 5.2 调度评估：Scheduler 六步

```
server/scheduler.go  Scheduler{s, pg, tick}
  newScheduler：tick = 5 * time.Second
  Run(ctx)：select{ ctx.Done / <-tick.C → reconcileConcurrency() + step() }
  init()：四个水位键（finding/timeout/toolcall/taskcreate）为空时
          取现有最大值落库──「only events created AFTER the scheduler first
          starts count」（重启不追历史）
 step()：ListEnabledTriggers() → fireIntervals / fireFindings / fireGoals
          / fireTaskTimeouts / fireToolCalls / fireTaskCreates

① fireToolCalls── finding 触发的实际主路径
     筛 tr.OnToolCall && len(tr.ToolNames)>0
         last := ParseInt(schedState["last_toolcall_id"])     ← 水位
         events := pg.NewToolCallsSince(last)
             │ SQL（db/triggers.go）：
             │   SELECT r.id, t.id, t.description, t.goal, r.tool,
             │          COALESCE(u.detail,''), COALESCE(r.detail,''), r.is_error
             │   FROM activity r
             │   JOIN tasks t ON t.exploration_id = r.exploration_id
             │   LEFT JOIN activity u ON u.exploration_id=r.exploration_id
             │        AND u.tool_use_id=r.tool_use_id AND u.kind='tool_use'
             │   WHERE r.kind='tool_result' AND r.id > $1 AND r.tool <> ''
             │     AND t.deleted_at IS NULL ORDER BY r.id
             │ ★触发只认任务活动流 activity 表 kind='tool_result' 行；
             │   conversations 有自己的活动表 → reporter 自己的工具调用不会自激
     if e.ToolIsErr && e.Tool=="report_finding" → continue
             （失败的 report_finding 没有已提交漏洞）
     消息拼装：
             msgCtx = "\n\n【本次由工具调用触发】\n工具: %s\n入参: %s\n返回: %s%s"
             （入参/返回各截 1500 rune；errTag="[error] "）
     if !containsFold(tr.ToolNames, e.Tool) → continue
             → StartTriggeredRun(tr.AgentKey,
                  "工具触发 · "+e.Tool+" · task#"+e.TaskID,
                  tr.ToolCallMessage + msgCtx,       ←★finding_id 在「返回」里
                  e.TaskID, true, e.TaskDesc, e.TaskGoal)
         SetSchedState(last_toolcall_id, maxID)   ← 无论有无匹配都推进水位

   finding_id 怎么进消息：report_finding 返回首行即
     "finding recorded: <nodeID>"（agent/tools.go），
   后随 RecordedFinding JSON（含 evidence_status/evidence_note 等）；
   tool_call_message 再指示 agent「读取返回 JSON 的 finding_id / finding_node_id」。

② fireFindings── on_finding=true 路径
   msgCtx = "\n\n【本次由任务发现 finding 触发】\n发现: [%s/%s] %s"
            （VulnClass/Severity/Summary）
   → StartTriggeredRun(…, tr.FindingMessage+msgCtx, e.TaskID, true, …)

③ fireGoals④ fireTaskTimeouts⑤ fireTaskCreates
   同模式：各自 message + 事件上下文。
⑥ fireIntervals：定时触发，ctx="\n\n【本次为定时触发】"+时间，
   taskID=0, mergeable=false。
```

### 5.3 触发执行链：入队 → 合并 → 会话

```
server/conversations.go StartTriggeredRun(agentKey, title, message,
                                              taskID, mergeable, taskDesc, taskGoal)
    pg==nil 或 chatAgent 未装配 → return
        cfg := s.readTriggerBehavior(agentKey)   ← DB read BEFORE the lock
    triggerCfg[agentKey]=cfg；triggerQ[agentKey] += triggeredRun{…}
    s.pumpLocked(agentKey)

triggeredRun 结构 ；triggerBehavior ：
  runMode   serial | parallel
  mergeMode by_task | all | none（serial 用；parallel 忽略，每条各自一会话）
  maxParallel parallel 并发上限，<=0 不限
readTriggerBehavior ：默认 {serial, all, 5}；仅 TriggerRunMode=="parallel" 切并行。

pumpLocked：
  limit := 1；parallel 时 maxParallel<=0 → MaxInt，否则 = maxParallel
  for triggerActive[agentKey] < limit && len(queue)>0 {
      item := s.nextTriggerRun(…); triggerActive++; go s.runAndPump(agentKey, item)
  }
runAndPump：跑完 --；空闲清理三张 map，否则 pumpLocked 补位

nextTriggerRun合并语义：
  parallel 或 mergeMode=="none" → 队首单条，不合并
  mergeMode=="all"      → 整个队列合并（mergeAllRuns ）
  mergeMode=="by_task"  → 队首 + 同 taskID 且 mergeable 的合并（mergeTriggeredRuns ）
                          头部 !mergeable 或 taskID==0 → 单跑
  消息 = taskContextHeader（：「【任务 #%d %s（目标：%s）】」，
         desc 截 200 / goal 截 500）+ "\n" + 事件体（finalTriggerMessage ）

runTriggeredRun：
    defer recover()（防 panic 卡死队列）
        c := pg.CreateConversation(agentKey, firstLine(title,60), nil)
            ★每次触发新建一个 conversation（第 3 参 llm profile=nil → 走 agent 绑定/全局；
              db/conversation.go）
    msg := finalTriggerMessage(item)；AppendConvActivity(c.ID,
            {Worker: agentKey, Kind: "user", Summary: firstLine(msg,200), Detail: msg})
    chatBusy[busyKey]=true; s.runConversationSync(c, msg, busyKey)
  会话互斥：chatBusy/chatCancel 以 convBusyKey(c.ID)="conv-<id>" 为键，
            触发器会话与人工会话共用同一互斥，一次只跑一轮。

runConversationSync→ conversationRunContext（：
  intercept.WithConvID + chatCancel[busyKey]）→ runConversationTurn（+）：
    ca := s.resolveChatAgent(c)（effectiveProfileForAgent 命中可换 profile）
    maxTurns := s.agentMaxTurns(c.AgentKey)；
            maxDuration := agentRunSeconds(c.AgentKey) * time.Second
    webSearch := a.WebSearch
        sessionID := convBusyKey(c.ID)   ← transcript 会话键
    emit → pg.AppendConvActivity（conversations 活动表！）
        ca.Chat(ctx, c.AgentKey, sessionID, msg, maxTurns, maxDuration, webSearch, emit)
        pg.TouchConversation(c.ID)
```

### 5.4 agent 运行上下文：能调工具吗？任务上下文从哪来？

```
agent/chat.go ChatAgent.Chat(ctx, agentKey, sessionID, message,
                                 maxTurns, maxDuration, webSearch, emit)
    工作目录 <workDir>/sessions/<sessionID>/；WithReviewWorkingDirectory
    ★工具底座：
            base := actool.DefaultTools()            ← Read/Write/Edit/Bash/…
            ctx = WithRunInfo(ctx, RunInfo{SessionID: sessionID})
            tools, def, cleanup := AugmentTools(ctx, agentKey, base)
            注释：「Pure assistant: DefaultTools as the base; AugmentTools layers in
            the key's visible skills/MCP and lets the DB tools table filter/override.
            DefaultTools have no tools-table rows, so they always pass through.」
    Options：PermissionMode Bypass / EnableWebFetch+代理 /
            EnableWebSearch（按 agent.web_search）/ BashEnv / WorkingDir /
            MaxTurns / MaxDuration（每轮重置）/
            Compaction / Todos / ToolOutputDir / Settlement /
            guard hooks / Transcript（c.tx!=nil 时）
    enableNoa / attachSideCapture / NewSession(opts)
    s.Resume(sessionID)；seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
            （重放历史 Skill 调用重新解锁 MCP，deferred.go）
            captureRunSession（emit 打 Worker=agentKey）
```

**任务上下文**：

```
agent/runinfo.go RunInfo{TaskID, ExplorationID, IntentID, SessionID}
  chat 路径只填 SessionID；TaskID/IntentID = 0   ← 无任务/意图运行时上下文
任务上下文只以文本存在：taskContextHeader「【任务 #N 描述（目标：…）】」+ 事件体
任务级工具（get_task_graph / get_task_node_detail / list_task_findings /
get_task_worker_trace / get_finding_traffic / update_finding_report …）
全部要求显式传 task id / finding id 参数。
```

### 5.5 内置实例：reporter agent 的完整预置（可照抄做 vuln_*）

```
server/orchestration.go  seedOrchestrationTools() 内：
  s.seedReporterAgent(); s.upgradeReporterTriggerMessage()

seedReporterAgent：
    幂等 flag "reporter_agent_seed_v1"（GetSetting guard）
    GetAgentByKey("reporter") 已存在 → 不覆盖（用户手建过）
    CreateAgent("reporter", "报告撰写", "漏洞详细报告撰写：…")
              └ db/config.go：role 硬编码 'assistant'，builtin=false，enabled=true
        SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt)
              └ 正文 agent/promptcatalog.go 起
    SetAgentTriggerBehavior("reporter", "parallel", "none", 5)
              注释：「一漏洞一报告、多个 finding 并发各写各的；merge 必须 none，
              否则一波 finding 被合并成一次运行，并行就没意义了」
    AddAgentToToolBinding("reporter", ["update_finding_report",
              "get_task_node_detail", "list_task_findings", "get_task_worker_trace",
              "list_task_worker_traces", "search_task_worker_traces", "get_task_graph"])
    CreateTrigger({AgentKey:"reporter", Enabled:true,
              OnToolCall:true, ToolNames:["report_finding"],
              ToolCallMessage: reporterToolCallMessage})   ←★on_tool_call 触发

reporterToolCallMessage（要点）：
  「上面刚有一个漏洞被 report_finding 登记。请读取返回 JSON 的 finding_id（独立漏洞
    记录 ID）与 finding_node_id（探索节点 ID），先用 get_finding_traffic(finding_id)
    读取当前证据清单及其 version（空清单是正常情况，照常写报告）……最后调用
    update_finding_report(finding_id=finding_node_id, report, evidence_version=实际读取
    版本) 保存，evidence_version 必须传，否则报告会被永久标记为待更新。
    不要混用两种编号。」
  （ 注释解释为何必须无条件先读 traffic）

upgradeReporterTriggerMessage：
  flag "reporter_trigger_evidence_version_v1"；只升级仍是旧文案 V1 且未改过的触发器
  （用户改过的不动）；V1→现行文案（补 evidence_version 回传）。

别处对 reporter 的运行时特判：agent/finding_workflow.go
  if agentKey=="reporter" && tool ∈ {traffic_search, traffic_get, traffic_blob} …
```

### 5.5b 内置实例：vuln_dispatcher 的预置与派发边界

`server/orchestration.go seedVulnDispatcherAgent` 在编排工具播种时一次性创建 `vuln_dispatcher`（settings flag `vuln_dispatcher_seed_v1`；已有同名 key 不覆盖，删除后不重建）。它是 `builtin=false` 的自定义 agent，但**由源码首启播种**，不是只能靠配置包导入的示例。

- 提示词由 `agent.VulnDispatcherDefaultPrompt` 经 `resolveVulnDispatcherPrompt` 替换五类场景模板占位符；找不到模板时 id 置 0，提示词要求省略该参数。
- 运行策略为 `serial + by_task + max_concurrent=0`（同任务 finding 串行、按任务合并）。
- 工具绑定为 `spawn_task`、`list_llm_profiles`、`get_task_node_detail`、`list_task_findings`、`get_task_graph`、`list_task_constraints`；`spawn_task` 是唯一写操作。
- 触发器为 `Enabled=true、OnFinding=true、FindingMessage=vulnDispatcherFindingMessage`。消息和提示词只允许 `getshell/rce/file_upload/deserialization` 且 `critical/high` 的 finding 进入派发判断；先查任务约束，再按 vulnclass/描述选场景，并做同立足点去重，其余直接结束。

这条链是「通用 on_finding 触发器 + LLM 判定 + spawn_task」，`report_finding` 本身没有 vulnclass 的代码分支。子任务审核开关仍由 `spawn_task` 的继承规则决定，dispatcher 不传 `review_enabled`。

### 5.5c 内置实例：retester 的手动复测链

`server/finding_retests.go seedFindingRetester` 首启播种 `retester`（`db.FindingRetestAgentKey`，`builtin=false`、enabled=true，flag `finding_retester_seed_v1`），同时播种两件专用工具并绑定到 retester；没有自动 finding 触发器。

HTTP 路由在 `server/server.go`：
`GET /api/exploration/findings/{id}/retests`（历史）、`GET /api/exploration/findings/retests/active`（进行中）、`POST /api/exploration/findings/{id}/retests`（发起）。

`startFindingRetest` 校验 finding、retester enabled、两件工具的 enabled+binding、chat provider 和服务状态；缺工具时返回 409「请为复测 Agent 启用并绑定工具：<key>」。`db.CreateFindingRetest` 对 finding 加行锁并保存证据快照；已有 pending/running 复测则返回 `created=false` 复用进行中记录，不重复 dispatch。新记录返回 202 `{retest,created:true}`，复用返回 200。

复测会话只能使用 `get_finding_retest_context` 与 `record_finding_retest_result`：两件工具从服务端 conversation context 取 finding，工具参数不能把结果写到别的 finding。结论为 `reproduced/fixed/inconclusive`；会话成功结束且 verdict=fixed 时才把原漏洞状态改为已修复，其余结论保留原状态。

### 5.6 触发器管理 HTTP API

```
server/server.go:
  GET    /api/agents/{key}/triggers   → pgListTriggers
  POST   /api/agents/{key}/triggers   → pgCreateTrigger
  PATCH  /api/triggers/{id}           → pgUpdateTrigger
  DELETE /api/triggers/{id}           → pgDeleteTrigger

server/triggers.go（头注释「P3 agent triggers (仅自定义 agent)」）：
  validateTrigger   至少一种触发条件，否则「至少选择一种触发条件(定时/发现finding/
                          目标达成/任务超时/工具调用/任务创建)」
                          OnToolCall ∧ len(ToolNames)==0 → 「工具调用触发至少选择一个工具」
  pgCreateTrigger   a.Builtin → 400「触发器仅支持自定义 agent」
                          （内置拦截仅 HTTP 层，DB 层无约束）
  pgUpdateTrigger  只按 {id} 定位，无 agent 归属/内置校验
```

### 5.7 关键澄清（二开最常误解的三点）

1. **「finding 触发」走的是哪条路径？** 两条独立路径都在 `server/scheduler.go`：
   - `on_tool_call` + `tool_names=["report_finding"]`（fireToolCalls）——内置 reporter 用的就是这条，能拿到完整工具入参/返回（finding_id 在返回里）；
   - `on_finding=true`（fireFindings）——直接监听 finding 落库事件，消息里只有 `[VulnClass/Severity] Summary`。
   内置 `vuln_dispatcher` 使用第二条 `on_finding` 路径（见 5.5b）；若另建 vuln_* agent 且需要完整工具入参/返回，才考虑 `on_tool_call`+`report_finding`。

2. **vulnclass 九类两档没有任何代码级派发。** 全仓 `P-ORCH·§4.2.8` 仅 3 处命中，全是字符串/注释：
   - `agent/tools.go` — report_finding 入参 schema 里给 LLM 看的词表描述（「严重档=…触发内网渗透类子任务」只是提示词约定）；
   - `server/orchestration.go` — schema 刷新 flag 名；
   - `server/orchestration.go` — refreshBuiltin 注释。
   `report_finding` 落库（`agent/tools.go`）只 Record + notifyFinding + notify，**无 vulnclass 分支**；严重档/高危档派生子任务完全靠 LLM 读提示词后显式调 `spawn_task`。

3. **`spawn_task` 默认只绑 `auto`，但内置 dispatcher 是明确例外。** 种子绑定 `server/orchestration.go`（全部 orchestration 工具绑 auto，例外 `bind_finding_traffic` 绑 reporter）；`seedVulnDispatcherAgent` 另给 `vuln_dispatcher` 预置 `spawn_task` 绑定。其它 mainagent/pentest/自建 vuln_* agent 默认拿不到 spawn_task，要在 tools 表绑定（运行时 `ToolResolve` 会丢弃未绑定项，见第 6 章）。

### 5.8 第 5 章方法速查

| 方法 | 位置 | 简介 |
|---|---|---|
| `Scheduler.Run/step/init` | `server/scheduler.go` | 5s tick；六类触发依次评估；水位初始化 |
| `fireToolCalls` / `fireFindings` | `server/scheduler.go` / — | 工具/finding 触发评估与消息拼装 |
| `NewToolCallsSince` | `db/triggers.go` | 工具事件源 SQL（activity tool_result 水位增量） |
| `StartTriggeredRun` | `server/conversations.go` | 入队 + readTriggerBehavior + pumpLocked |
| `pumpLocked/nextTriggerRun` | — / — | 并发上限与合并语义（all/by_task/none） |
| `runTriggeredRun` | — | 新建 conversation + 追加消息 + runConversationSync |
| `runConversationTurn` | `+` | 取 ChatAgent/预算/webSearch → ca.Chat → Touch |
| `ChatAgent.Chat` | `agent/chat.go` | 触发器/人工会话共用执行体 |
| `seedReporterAgent` | `server/orchestration.go` | 内置 reporter 预置（parallel+none+5 并发范本） |
| `seedVulnDispatcherAgent` | `server/orchestration.go` | 内置 dispatcher、6 件工具与 on_finding 触发器预置 |
| `seedFindingRetester` / `startFindingRetest` | `server/finding_retests.go` | retester 首启播种与漏洞详情手动复测 |

---

# 第 6 章 · skill / tool / MCP 在任务运行时的配合

### 6.0 装配总入口：AugmentTools 四步

```
agent/assembly.go  AugmentTools(ctx, agentKey, base []actool.CoreTool)
                      → ([]actool.CoreTool, DeferredInfo, cleanup)
base = 该角色的内置工具（代码层，永不过滤， 注释）；
cleanup 必须 defer（关 MCP 客户端）。

① 增量注入     ToolAugment(ctx, agentKey) → 追加 Skill 元工具 + MCP 工具 + host 工具
                （包级钩子 assembly.go，由 server 侧 wireAgentAugment 绑定）
② 工具表裁量   ToolResolve(ctx, agentKey, out)
                （实现 server/assembly.go：按 DB tools 表丢弃未绑定/禁用、
                 覆盖描述/schema、注入 schema 默认值；无 DB 行的名字原样穿透 ）
③ finding 整形    findingWorkflowTools(agentKey, out)（agent/finding_workflow.go）：
                traffic 绑定开启时给 report_finding/add_hint/add_task_hint 追加使用说明，
                关闭时删 bind_finding_traffic / reporter 删 traffic_*，并剥离 schema 里的
                traffic 参数；说明串回填 def.FindingGuidance
④ panic 兜底   每个工具包 guardPanic（ → guardedTool.Call ，
                recover 后返回 actool.Errorf，err=nil）

DeferredInfo（assembly.go）：
  FindingGuidance string      ← ③ 回填
  Deferred        []string    ← 全部 MCP/host 工具名（schema 被隐藏）
  GlobalNames     []string    ← 只进 system prompt <available-deferred-tools> 名单
  Unlock          *actool.UnlockSet  ← 共享调用闸（unlock 与 deferred 分离）
  UnlockSkill     func(skillName string)

最终 system prompt 拼接（agent/deferred.go deferredSystem）：
  sysText += def.FindingGuidance          ()
  block := RenderDeferredToolsBlock(def.GlobalNames)   ()
  无 block → system=[sysText], boundary=0              ()
  有 block → system=[sysText, block], boundary=2       ()
  随后 opts.SystemPrompt=system; DynamicBoundary=boundary;
  Tools=tools; DeferredTools=def.Deferred; UnlockSet=def.Unlock
调用点：worker.go / planner.go / mainagent.go / chat.go / customexec.go
```

### 6.1 skill 体系：内容在文件系统，DB 只管可见性与账本

```
skill 内容 = <skillDir>/<dir>/SKILL.md（YAML front matter + 正文）
解析器 third_party/norma/skill/skill.go Skill{...MCPs []string...}
  LoadDir ，parse

DB 两张表（db/schema.sql）：
  agent_skill_visibility(agent_id FK agents, skill_name, enabled, PK(agent_id,skill_name))
  skill_usage(id, ts, skill, agent_key, task_id, exploration_id, intent_id,
                  session_id, args_len, found)
       found=false = 模型点名了不存在的技能，刻意保留作补技能依据

内置可见性种子（db/db.go）：
  builtinSkillVisibility = {"api-recon": ["auto","pentest","worker"]}
  seedBuiltinSkillVisibility → INSERT ... SELECT ... ON CONFLICT DO NOTHING
CRUD（db/config.go）：
  AgentSkillNames  / SkillAgents  / SetAgentSkillVisibility
  （先 DELETE 全删再 INSERT ON CONFLICT DO UPDATE）/ ToggleSkillVisibility  / Delete
HTTP（server/server.go）：上传/删除/缺失统计/用量统计/元数据/文件管理
  handler 在 server/server_mgmt.go（fsListSkills、fsUpdateSkillMeta、
  fsUploadSkill、fsWriteFile）
```

**加载进某次运行**（`server/assembly.go`）：

```
  流程：pg.AgentSkillNames(a.ID) 取启用名单
         → skill.LoadDir(skillDir) 全量扫描
         → 按【目录名 filepath.Base(s.Dir)】匹配（不是 display Name）
         → 装进新 skill.Registry；一个都没匹配上则 reg=nil

同一闭包内的工具增强（server/assembly.go）：
  gated 集合 = 所有可见技能 MCPs 字段点名的 server
        → 这些 server 的工具 deferred + 锁定，不进全局 deferred 块
 unlock := actool.NewUnlockSet(globalNames...)
         unlockSkill(name) = 把该技能声明的 server 的全部工具 unlock.Add
 reg.OnInvoke = func(s skill.Skill) string {
             unlockSkill(s.Name)
             return actool.RenderDeferredToolsBlock(reveal)   ← ack 里播名字
         }
     技能本体作为【一个】元工具注入：
         extra = append(extra, meterSkillTool(reg.Tool(), pg, reg, a.Key,
                                              agent.RunInfoFrom(ctx)))
```

**Skill 元工具本体**（`third_party/norma/skill/skill.go`）：

```
name = "Skill"；Prompt 列出技能清单（maxSkillListingChars=8000 / maxSkillDescChars=500 ）
调用：
  unknown skill → tool.Errorf("Error: unknown skill %q. Available: %s")
  ${SKILL_DIR} 替换为 s.Dir
  正文以 llm.SkillInvocationMessage(...) 作为【独立 guidance 消息】返回（Extra ，
  可被 compaction 重放）── 即加载技能 = 往上下文注入一份说明书，同时解锁它的 MCP 工具

续跑恢复（agent/deferred.go seedUnlockFromHistory）：
  重放历史里 name="Skill" 的 tool_use，重新 unlockSkill
  调用点：mainagent.go（每轮新会话故每轮重放）/ worker.go,516 / chat.go

计量（server/skill_usage.go meterSkillTool）：
   Call 先 record 再转发； record 把 display Name 归一成目录名，
  未命中记 found=false（best-effort，失败只 log）
```

### 6.2 工具注册体系

**代码层工具表**（`agent/toolcatalog.go builtinToolsByAgent`）：

```
"mainagent" → ts.MainAgentTools()
"planner"   → ts.PlannerTools()          （agent/tools.go）
"worker"    → ts.WorkerTools()           （agent/tools_insert.go）
"goals"     → {setGoals, setConstraints}
"auto"      → {addFinding, insertAssets, addCompanyScope, listAssets, listCompanies}
"pentest"   → {listAssets, insertAssets, addFinding, listFindings, listCompanies}
刻意不含 actool.DefaultTools()（Read/Write/Edit/…）

toolcatalog.go var defaultUnbound = {"goal_met": true}
  → 进 tools 表但默认不绑定任何 agent（Agents 强制 []）
BuiltinToolSeeds() 去重顺序：mainagent→goals→planner→worker→auto→pentest，同名合并 Agents

ToolSet 组装：NewToolSet(store, worker)（agent/tools.go，字段含 checkExecutor）
  其余入口：WorkerTools tools_insert.go / MainAgentTools  / AllDomainTools
  ReviewTools tools_review.go = ReviewReadOnlyTools + setIntentStatus/sendPlannerHint/
  addReviewIntent/updateIntent/cancelIntent
  复测专用：server/finding_retests.go findingRetestTools =
    get_finding_retest_context + record_finding_retest_result（只绑定 retester，按会话上下文定位 finding）
  域工具注册表：server/assembly.go buildDomainReg（AllDomainTools 挂上 AssetStore）
```

**落库 + 运行时解析**（`server/assembly.go`）：

```
  启动播种：agent.BuiltinToolSeeds() 首插 tools 表（ON CONFLICT DO NOTHING）
  traffic.SeedToolMetas()（默认绑 worker）
  agent.ToolResolve（fail-open 裁量器）：
    pg.ListTools() 失败 → log「[tools] 读取工具表失败，按代码默认放行」原样返回
    有行的按 !row.Enabled || !contains(row.Agents, agentKey) 丢弃
    域工具按 Enabled && bound && row.Kind!="shell" && !inList 追加
    kind="shell" 的行把提示文本追加进 Bash 描述
    !InteractiveShellDisabled() && agent.InteractiveShell 时追加 ShellSessionTools()
           （shell_open/shell_send 等）
```

### 6.3 MCP：配置在 DB，发现时缓存，装配时接入

```
DB（db/schema.sql）：
  mcp_servers(id, name UNIQUE, transport CHECK IN ('stdio','http','sse'),
                  command, args JSONB, env JSONB, url, enabled, insecure, ...)
  mcp_tools_cache(server_id FK ON DELETE CASCADE, tool_name, description, schema,
                      UNIQUE(server_id,tool_name))
  agent_visibility(agent_id, resource_kind='mcp', resource_id, mcp_tool_name, enabled)
Go 侧：db/config.go MCPServer；ListMCP  / MCPToolNames  /
      MCPToolsDetailed  / SaveMCPTools (DELETE+INSERT) / SaveMCP  /
      DeleteMCP （同事务先删 agent_visibility 再删 server）

HTTP（server/server.go → server/server_mgmt.go）：
  pgListMCP  / pgSaveMCP  / pgDeleteMCP  / pgRefreshMCP  / pgMCPTools
  pgSaveMCP：isNew && Enabled 时同步跑一次 discoverAndCacheMCP（45s 超时，失败只 log）
  pgRefreshMCP：找不到 404「MCP 不存在」；发现失败 502「工具发现失败：」
  启动补漏：server/server.go go s.discoverEmptyMCPsOnStartup()
            （只补「启用且缓存为空」的 server，90s 超时）

连接（server/mcpdiscover.go connectMCP）：
  stdio 需 command 否则「stdio 传输缺少命令」→ mcp.NewStdioClient
  http  需 url    否则「http 传输缺少 URL」  → mcphttp.New(ctx,name,url,headers,Insecure)
  sse                                        → mcphttp.NewSSE
  其它 → 「未知传输方式 %q」
discoverAndCacheMCP  = connect → Tools(ctx) → pg.SaveMCPTools
```

**工具如何进入 agent 工具表**（`server/assembly.go`）：

```
  流程：
  pg.AgentVisible(a.ID, "mcp") 得直连白名单（want）
  遍历 pg.ListMCP()：
    !m.Enabled → continue
    directVisible := want[m.ID]
    skillGated  := gated[m.Name]     ← 6.1 里可见技能 MCPs 点名的集合
    两者皆假 → continue
    连接失败 log「[mcp] %s 连接失败」并 continue
    工具逐个 append 进 extra（→ 进工具表）并记入 allNames（→ DeferredTools 隐藏 schema）
    仅 directVisible && !skillGated 的进 globalNames
      （→ system prompt 全局块 + 开局即 unlock）
  host 工具同理（deferredBinds 只对绑定到本 agent 的名字 defer）
装配入口绑定：server/assembly.go wireAgentAugment(m.pg, s.skillDir, s.hostTools)
```

### 6.4 各 agent 的装配单点：agentsForTask（`server/task_llm.go`）

```
  结果缓存 s.taskAgents[t.ID]（invalidateTaskAgents  / taskRuntimeAvailable ）
           建 4 个 runtime：agentKey = "goals" / "planner" / "worker" / "mainagent"
           ★agentKey 决定 Skill/MCP/工具表裁量口径
   wk = agent.NewWorker(...) + SetFindingRecorder / SetCompactionWindowResolver /
           SetNonStreaming / SetMaxTokens / SetNoaEnabled / SetRunTimeout / SetProxy /
           SetWebSearch / SetConstraintInject
   pl = NewPlanner(...) + 同上 + SetKillWork / SetSteerWork /
           SetNotifyReview(→ wakeReviewRunner) / SetReviewEnabled /
           SetExecutorChecker(s.checkExecutorKey) / SetTaskPromptOverride /
           SetCompactor
   main = NewMainAgent(...) + 同上基础集

每次运行的 base 工具集（随后进 AugmentTools 的 base 参）：
  worker     tsx.WorkerTools() + w.extraTools + defaultToolsExcept("MultiEdit","Glob","Grep")
             （agent/worker.go）
  planner    DropCoverageTools(...) + actool.DefaultTools()（agent/planner.go）
  mainagent  DropCoverageTools(...) + actool.DefaultTools()（agent/mainagent.go）
  chat       actool.DefaultTools()（agent/chat.go）
  customexec tsx.WorkerTools() + defaultToolsExcept(...)（agent/customexec.go）
review 对话轮单独装配：agent/tools_review.go RunReviewChat
  NewToolSet(store,"review") + SetExecutorChecker() + ts.ReviewTools()()
  + intercept.WithTaskContext(ctx, cfg.TaskID, "review", cfg.Emit)()
  + Hooks（server/review.go Hooks: t.Guard.Hooks()）
```

### 6.5 方法速查

| 方法 | 位置 | 简介 |
|---|---|---|
| `AugmentTools` | `agent/assembly.go` | 唯一工具装配入口（注入→裁量→finding 整形→panic 兜底） |
| `deferredSystem` | `agent/deferred.go` | system prompt 拼接 deferred 块 |
| `seedUnlockFromHistory` | `agent/deferred.go` | 重放历史 Skill 调用解锁 MCP |
| `skill.Skill` 元工具 | `third_party/norma/skill/skill.go` | 加载技能=注入说明书+解锁其 MCP |
| `meterSkillTool` | `server/skill_usage.go` | skill 调用计量（found=false 留作补技能依据） |
| `builtinToolsByAgent` | `agent/toolcatalog.go` | 代码层工具表（不含 DefaultTools） |
| `ToolResolve` 实现 | `server/assembly.go` | DB tools 表裁量（fail-open） |
| `wireAgentAugment` | `server/assembly.go` | 装配钩子绑定（skill+host+MCP） |
| `connectMCP/discoverAndCacheMCP` | `server/mcpdiscover.go` | MCP 连接与工具缓存 |
| `agentsForTask` | `server/task_llm.go` | 每任务 4 个 LLM runtime 单点 |

---

# 第 7 章 · 全项目门禁总表

### 7.1 HTTP 入参校验（createTask：`server/server.go`）

| 位置 | 条件 | 拒绝行为 |
|---|---|---|
| `server/server.go` | body JSON 解码失败 | 400 `"bad json: …"` |
| `server/server.go` | `validateTaskProfileIDs`：id≤0/重复/`loadProfileConfig` 失败 | 400 `"LLM 配置 id 无效或重复"` / `"LLM 配置 #%d 不存在或未设置 API Key"` |
| `server/server.go` | `TimeoutSeconds < 0` | 静默归 0（非拒绝） |
| `server/server.go` | `len(SourceTaskIDs) > db.MaxTaskSourceCount` | 400 `"关联任务最多选择 %d 个"` |
| `server/server.go` | 关联任务 id 解析失败/≤0/重复/不存在 | 400 `"关联任务 id 无效或重复"` / `"关联任务 #%d 不存在"` |
| `server/server.go` | `NormalizeTaskCompanyIDs` 失败 | 400 `"关联企业无效：最多选择 %d 个有效企业"` |
| `server/server.go` | `asset_mode` 超 64 字符 | 400 `"asset_mode 最多 64 字符"` |
| `server/server.go` | `buildTaskInterceptRules` 失败 | 400 `"任务级拦截规则无效：…"` |
| `server/server.go` | `pre_agent` 非空但 agent 查不到/未启用/内置 | 400 `"前置执行器 %q 不可用（须为已启用自定义 agent 的 executor_key）"` |
| `server/server.go` | `planner_prompt` 非法（空/>32KB/语法错/试渲染失败） | 400 `"规划提示词覆盖无效：…"` |
| `server/server.go` | `planner_template_id>0` 但模板不存在 | 400 `"规划模板 #%d 不存在；可用模板：…"` |
| `server/server.go` | `WorkerCount < 0 || > 16` | 400 `"worker_count 取值 0~16（0=跟随全局）"` |
| `server/server.go` | 任务分类/企业无效 | 400 `"任务分类不存在或无效"` / `"关联企业不存在或无效"`；其它 500 |

`validatePlannerTemplateBody`（`server/planner_templates.go`）：空 → `"模板正文不能为空"`；
>32KB（`db/config.go`）→ `"模板正文最多 %d 字节"`；`missingkey=error` 解析失败 → `"模板语法错误"`；
全字段假 `agent.PlannerVars` 试渲染失败 → `"试渲染失败（合法变量：…）"`。
模板库 create/update/dry-run 同标准（`server/planner_templates.go`）；
DB 层再兜 `db/config.go`；请求体上限 `MaxBytesReader` 413（—）；
内置只读/默认保护 → 409（`writePlannerTemplateErr `）。

并行入口 `spawn_task`（`server/orchestration.go`）：`pre_agent` 无效 → actool.Errorf；
`planner_prompt` >32KB；`validatePlannerTemplateBody`；
`worker_count` 越界。

### 7.2 任务准入：`admitTask/admitTaskWhen`（`server/goals.go`）

```
  admitTask → admitTaskWhen(t, mode, false)； admitPausedTask → requirePaused=true
  判断链：
  t==nil → "task not found"
  Task(t.ID) 不存在或指针已换或 IsDeleting → "task is being deleted"
  !beginTaskOperation(t.ID)（engine_timeout.go 删除屏障）→ "task is being deleted"
  requirePaused：终态 → "终态任务不能执行继续"；!Paused → "仅已暂停的任务可以继续"
  已准入任务（running 等）→ 只做 wake-up + ApplyTaskAdmission CAS，不重复占槽
  shouldQueue := enabled && (!ready || readyBacklog || atCapacity)
    → engine.Pause("queued_for_admission", …)
      排队文案 ：并发上限 / 无可运行 LLM 配置 / FIFO 更早任务
  ★整个判断+提交在 s.concMu 下串行，删除屏障也在此锁下安装
  槽位占用判据 occupiesConcurrencySlot：
  Queued/Paused/终态 → false；IsDeleting → true（保留到删除提交）；
  否则 !IsPaused && (ReadyFor || ActiveLLMCalls>0)
调用点：goals.go(bootstrap)/(resume)、server.go、findings_groups.go
```

### 7.3 资产范围拦截：`CheckAssetsIntercept`（`db/asset_intercept_match.go`）

```
  len(ids)==0 → 放行
      blockRules = ListAssetInterceptRules()（全局）
      taskID>0 追加任务级 block+allow（TaskInterceptRulesSplit）
      ErrNoInterceptRules 被吞
  无 block 且无启用 allow → 全放行
  GetByIDs 全局查询（不受任务 scope 削弱）
  EvaluateAssetGate(blockRules, allowRules, domains, ips, urls) 不通过 → 记 hit
触发者：
  agent/tools.go  createIntent 内核：命中 → 返回错误串
    「意图「%s」绑定的资产未通过测试范围校验，请停止对相关资产进行测试：」+ 逐条 h.Describe()
  agent/tools_intent.go  update_intent 改 asset_ids 同口径（"资产拦截校验失败：%v"）； 命中列表回给模型
```

### 7.4 引擎层门禁（plannerLoop `server/engine.go` / workerLoop —）

| 位置 | 条件 | 行为 |
|---|---|---|
| — | planner 快照 nil | return（idle：LLM 未配置） |
| — | `IsPaused` | return |
| — | `IsDeleting` | return（删除屏障） |
| — | 终态 Status | return |
| — | `isSettling` | return（收尾中） |
| — | `HasProposedIntent()` | log「planner 规划轮抑制：有待审意图」+ drainTriggersRetaining("revision_requested","hint") + return |
| — | 待审 + goalless | 豁免收尾，保持 running |
| — | 无待审 ∧ 无 open goal ∧ 无活跃意图 | drainTriggers → `SetTaskStatusGuarded(done)` + activity，永不进 Plan |
| — | `!beginTaskOperation` | return |
| — | ectx.Err() 或 IsDeleting | return |
| — | Plan 判定 met 但仍有被驳回意图 | log「目标达成判定被拦截」；否则 SetTaskStatus(done) + cancelExec(AbortGoalMet) |
| — | 心跳：无活跃意图但有待审意图 | log「planner 心跳抑制」+ continue |
| — | `planHeartbeatSeconds < 600` | 抬到 600（MinPlanHeartbeatSeconds） |

workerLoop：快照 nil → sleep 1500ms 重试；IsPaused → sleep 1000ms；
IsDeleting → return；isSettling/终态 → sleep continue；
`!beginTaskOperation` → return；`claimNext` 未领到 → sleep 800ms。
领取闸：`engine.go claimNext` → `Frontier(20)` 逐个 `ClaimIntent`。

### 7.5 审核闸门（详见第 2 章；此处只列门禁面）

```
待审单源过滤器（db/exploration.go proposedPendingCols）：
  kind='intent' AND state='proposed' AND payload->>'executor'='review'
  AND COALESCE(payload->>'review_status','pending')='pending'
  消费者：HasProposedIntent  / db/tasks.go TasksWithProposed（t.deleted_at IS NULL
  AND t.review_enabled AND proposedPendingCols("n")，server/review.go）

审核写 CAS（db/exploration.go approveIntentAt）：
  FOR UPDATE 读到 state='proposed' && executor=='review' && review_status=='pending'
  否则 ErrNotPendingReview；curVersion!=expectedVersion → ErrStaleVersion；
  批准即转正（executor = payload.pending_executor）；content_version+1
  RequestRevisionAt （note 必填、revision_count+1、diagnoseRevisionMiss 分三类）
  哨兵：ErrIntentNotFound/ErrNotPendingReview/ErrStaleVersion/ErrNoteRequired

槽位租约（每任务 review 单飞，进程内第 1 层）：
  server/review.go reviewSlot.acquire(owner, ttl)：busy 且未过期 → false；
  过期 → WARN「review 槽位被抢占 ⚠ 前 holder=%s 租约过期未释放（疑似崩溃）」并抢占
  门禁点 ：slot.acquire("prescreen", 5min) 失败 → 「预审跳过：review 槽位被占用」return
  前置：FreshPendingIntents(50) 为空 return；reviewProvider()==nil → log 并 return

送审双条件写入（agent/tools.go）：
  if proposed { payload["review_status"]="pending"; payload["executor"]="review" }（强制，禁止覆盖）
```

### 7.6 拦截器与 guard（详见 2.8；此处只列门禁面）

```
规则模型（intercept/intercept.go）：按 priority 降序评估；allow 立即放行跳过后续；
  deny 拦截回消息；ask 建 intercept_pending + 写 activity 等人工
  Match （首个命中即返回）；ruleMatches （match_target ∈ {tool_name, tool_input}，
  match_type ∈ {string, regex}（string 走 strings.Contains 子串匹配）；坏正则加载时被跳过 ）

启用集闸：IsToolEnabled ；默认硬编码 defaultEnabledTools =
  {Bash,WebFetch,web_search,shell_open,shell_send,Write,Edit,MultiEdit}
  intercept_enabled_tools JSON 解析失败 → 空集（等于全不拦）

种子 ask 规则（server/review.go seedReviewIntercept，`server/server.go` 启动期调用，幂等）：
  ① ReviewWriteTools（intercept/intercept.go）append 进启用集
  ② 逐工具种「[内置] 审核写动作确认」规则：match_target=tool_name、match_type=string、
    action=ask、priority=100、timeout 300s、timeout_action=deny
内置 deny 规则种子（db/db.go）：防火墙清库 priority 100；DROP/TRUNCATE/Mongo drop/Redis FLUSH
  priority 90；curl DELETE/HTTP DELETE/清空接口 priority 80；插入语句 timeout 60 deny

来源校验旁路：intercept/intercept.go SkipReviewGateFor(ctx, toolName)
  工具属审核写名单且 ctx agentName!="review" → true（planner 送审不弹卡）

guard preToolUse 评估链（guard/guard.go）：
   抽 Bash/shell_open/shell_send 的 command 供审计
   record(allow) → applyIntercept：
   interceptor==nil → 放行
    SkipReviewGateFor → 放行
   !IsToolEnabled → 放行
    Match
   未命中 → Judge（intercept/intercept.go），judged==false → 放行
   分派：
    deny → Log(denied) + block（前缀「【ARTEX 平台管控·非目标防御】此调用被平台拦截。原因：…」）
    allow → Log(allowed) + 放行
    ask → ctx.Err()!=nil 时 block「工作已取消，平台安全管控阻止执行」；
          否则 HandleAsk，false → block「人工审批未通过（用户拒绝或审批超时）」

LLM 判官兜底（intercept/intercept.go Judge）：
  !cfg.Enabled || reviewer==nil → (Decision{}, false)（放行）
  BuildReviewInput 失败 → ask + ModelFallback
  模型报错/输出不可解析 → fail_action（默认 defaultJudgeFailAction="allow"）
  默认：defaultJudgeTimeoutSecs=15 / defaultJudgeAskTimeoutSecs=300 /
        defaultJudgeAskTimeoutAction="deny"；非法配置回退默认

ask 挂起与超时（intercept/intercept.go HandleAsk）：
  建 pending 失败 → return false（拒绝）
  TimeoutEnabled=false → 无限等（ctx 取消 → ResolveIntercept(denied) + false）
  有超时：secs<=0 → 60；超时 → allowed := TimeoutAction=="allow"
  ResolveIntercept(pendingID,"timeout",action,"审批超时，按超时策略处理")
  Decide：!resolved → ErrAlreadyDecided「审批已处理或不存在，请刷新记录」

HTTP decide：server/server.go POST /api/intercept/pending/{id}/decide →
  server/intercept.go interceptDecide：bad id 400 / decode 400 /
  decision 非 allowed|denied 400 / ErrAlreadyDecided 409 / 成功 200 {"ok":true}
  规则 CRUD ；资产规则 ；pending/history/tool-config/judge ；任务级

审计不留孤儿：guard/guard.go deny 也 Log；intercept/intercept.go Log 写「已决定」行
postToolUse（guard/guard.go）只对 Bash 分类 blocked|error|ok（reBlocked ）做归因计数
```

### 7.7 executor 路由三层校验（`server/engine.go resolveExecutor`）

```
  key=="" → "executor_key 为空"
  GetAgentByExecutorKey 报错 → "查询执行器失败"
  查不到 → "执行器标识 %q 未注册"
  !Enabled → "执行器 %q 所属 agent 已禁用"
  Builtin → "内置 agent 不能作为自定义执行器"
R6 回退：resolveExecutor 失败 → activity + WARN log，回退默认 worker（不失败）
同校验器复用：planner 侧 SetExecutorChecker（task_llm.go → review.go checkExecutorKey）
  送审工具 tools_review.go；update_intent（tools_intent.go）；
  createTask pre_agent（server.go）
```

### 7.8 数据库层 CAS

```
claim CAS（db/exploration.go ClaimIntent）：
  UPDATE … SET state='running', owner=$1 WHERE id=$2 AND exploration_id=$3
         AND kind='intent' AND state='open'
  仅 RowsAffected()==1 视为领到（抢不到 false，调用方 sleep 重试）

状态机白名单（db/intent_edit.go SoftDeleteIntentAt）：
  open/proposed 放行；running/paused → ErrIntentNotEditable「意图 #%d 正在执行（state=%s），
  请先用 control_work 停止执行再取消」；其它 → ErrIntentNotEditable
  expectedVersion!=curVersion → ErrStaleVersion「expected=%d current=%d（先 list_intents 取最新 content_version）」
  空 reason → 兜底「planner 主动取消」；写 state='deleted', content_version+1

产出锁（db/intent_edit.go）：
  SELECT EXISTS(... exploration_edges WHERE src_id=$2 AND rel IN ('yields','spawns'))
  → ErrIntentProduced「intent already produced output; cancel and resubmit instead」
  前置门禁：kind!="intent" → ErrIntentNotFound；
  state!="open" && !isRejected：proposed(pending) → 「意图 #%d 正在审核中（pending），
  等审核结论或先驳回」；版本 CAS；summary 空 → "summary 不能为空"

意图创建门禁（agent/tools.go createIntent）：
  summary 空；parent 不存在；parent 非 fact/finding；
  priority 越界静默夹 1..10；资产拦截（见 7.3）

任务状态 CAS/守卫（server/manager.go）：
   ApplyTaskAdmission(id, expectedStatus, status, queued, mode, preservePosition)
   SetTaskStatusGuarded(id, status) (won bool, err)
```

### 7.9 其它门禁点

```
删除屏障：server/engine.go BeginDelete（deleting.LoadOrStore 已被占 → false；
  成功则 paused=true + cancelExec(AbortTaskDeleted)）； IsDeleting
  所有循环/准入/意图领取先过 beginTaskOperation（engine_timeout.go，
  RLock 下检查 IsDeleting 再 inflight+1）

prompt 渲染回退：agent/prompt.go renderSystem
  DB 模板渲染失败 → 代码默认→ 再失败退回原始 def
  均 missingkey=error（ renderTmpl）

模板/工具 schema 校验：agent/toolcatalog.go DecorateTool（空描述/schema 回退）
   injectDefaults（只在字段缺失/null 时注入，isEmptyJSON 视 "null" 与 "" 为空）

认证闸：server/auth.go requireAuth
  非 /api/auth/* 且非 /api/health：无 token → 401「未授权」；JWT 失败 → 401「token 无效或已过期」
  token 来源 extractToken（：Bearer / artex_token cookie / ?token=）

运行预算闸：server/server.go agentMaxTurns（0=不限）/  agentRunSeconds（兜底 1200）
  任务级 TimeoutSeconds（<0 归 0）+ engine.taskDeadline（engine_timeout.go）

panic 兜底闸：agent/assembly.go + 75-87（工具 panic 变普通工具错误，不炸会话）

guard hooks 挂载点：server/manager.go Guard: guard.NewWithInterceptor(ic)（NewManager 内装配）
  引用：engine.go（steerHooks 包裹）、chat.go、review.go
```

---

# 附录 · 关键函数索引

按包分组，位置 = `文件路径` + 函数/语句锚点（行号已移除；请在对应文件中搜索符号）。以下均以发布快照 commit `1902e43` 为准。

### A.1 server 包

| 函数 | 位置 | 简介 |
|---|---|---|
| `createTask` | `server/server.go` | HTTP 建任务入口（全部入参校验 + 落 Task + launchTask） |
| `launchTask` | `server/goals.go` | 种子意图开关 + admitTask("bootstrap") |
| `seedFirstIntent` | `server/server.go` | 种子意图写入：payload.executor=PreAgent、永远 open |
| `admitTask/admitTaskWhen` | `server/goals.go` | 任务准入：删除屏障/排队/并发槽/终态校验 |
| `plannerLoop` | `server/engine.go` | 每任务 planner 循环（待审抑制/收尾守卫/心跳 600s） |
| `workerLoop` | `server/engine.go` | 每 worker 领取循环（claimNext + 800ms 退避） |
| `runIntent` | `server/engine.go` | 执行分发：executor 路由 vs 默认 worker + model_error 重试 |
| `resolveExecutor` | `server/engine.go` | executor_key 三层校验（复用于 6 处） |
| `claimNext` | `server/engine.go` | Frontier 领取（ClaimIntent CAS） |
| `Run/step/init` (Scheduler) | `server/scheduler.go` | 5s tick；六类触发；水位初始化 |
| `fireToolCalls/fireFindings` | `server/scheduler.go` | finding 触发两大路径评估与消息拼装 |
| `StartTriggeredRun` | `server/conversations.go` | 触发器入队 + pump |
| `pumpLocked/nextTriggerRun` | `server/conversations.go` | 并发上限与合并（all/by_task/none） |
| `runTriggeredRun/runConversationTurn` | `server/conversations.go` | 触发会话：新建 conversation + ca.Chat |
| `seedReporterAgent` | `server/orchestration.go` | 内置 reporter 预置（parallel+none+5 + on_tool_call 触发器） |
| `seedReviewIntercept` | `server/review.go` | 审核写动作 ask 规则种子 |
| `ReviewRunner.Run/preReviewTask/reviewChat` | `server/review.go` | 审核轮：预审 → 送审/驳回 → 对话批准 |
| `interceptDecide` | `server/intercept.go` | HTTP 审批 decide（409 已处理/200 ok） |
| `requireAuth` | `server/auth.go` | JWT 认证闸（401） |
| `agentsForTask` | `server/task_llm.go` | 每任务 4 个 LLM runtime 单点 |
| `ToolResolve` 实现（赋值点） | `server/assembly.go` | DB tools 表裁量（fail-open） |
| `wireAgentAugment` | `server/assembly.go` | skill/host/MCP 装配钩子绑定 |
| `BeginDelete/IsDeleting` | `server/engine.go` | 删除屏障 |
| `beginTaskOperation` | `server/engine_timeout.go` | 循环级删除护栏 + inflight 计数 |
| `ApplyTaskAdmission` | `server/manager.go` | 状态 CAS + queued 位 |
| `SetTaskStatusGuarded` | `server/manager.go` | 带前提的收尾 CAS |

### A.2 agent 包

| 函数 | 位置 | 简介 |
|---|---|---|
| `Worker.Execute/ExecuteWithMessage` | `agent/worker.go` | worker 执行体（FindingRecorder/约束块/压缩） |
| `Planner.Plan` | `agent/planner.go` | planner 执行体（base 工具 + AugmentTools） |
| `MainAgent.Chat` | `agent/mainagent.go` | mainagent 执行体（每轮新会话） |
| `ChatAgent.Chat` | `agent/chat.go` | 会话执行体（触发器/人工共用） |
| `RunCustomExecutor` | `agent/customexec.go` | 自定义执行器（无 FindingRecorder/无态势概览） |
| `AugmentTools` | `agent/assembly.go` | 唯一工具装配入口（四步） |
| `deferredSystem/seedUnlockFromHistory` | `agent/deferred.go` | system 拼接 deferred 块 / 历史重放解锁 |
| `builtinToolsByAgent` | `agent/toolcatalog.go` | 代码层工具表 |
| `renderSystem` | `agent/prompt.go` | system prompt DB 覆盖 + 渲染回退链 |
| `createIntent` | `agent/tools.go` | 意图创建门禁（summary/父节点/优先级/资产拦截） |
| `report_finding` | `agent/tools.go` | finding 落库 + notifyFinding/notify |
| `UpdateIntent` | `db/intent_edit.go` | 六步编辑事务（锁/状态门/CAS/产出锁/patch/账本） |
| `RunReviewPreScreen/RunReviewAuto/RunReviewChat` | `agent/tools_review.go` | 审核三通道执行体 |
| `ReviewTools` | `agent/tools_review.go` | 审核工具集 |
| `Guard.PreToolUse/applyIntercept` | `guard/guard.go` | 拦截器评估链 |
| `Match/Judge/HandleAsk/Decide` | `intercept/intercept.go` | 拦截器匹配/LLM 判官/ask 挂起/人工决定 |
| `findingWorkflowTools` | `agent/finding_workflow.go` | finding 工具整形（traffic 绑定相关） |

### A.3 db 包

| 函数 | 位置 | 简介 |
|---|---|---|
| `AddNode/AddIntent` | `db/exploration.go` | 节点/意图落库（intent 永远 open） |
| `ClaimIntent` | `db/exploration.go` | 领取 CAS（open→running） |
| `SetNodeState/SoftDeleteIntentAt` | `db/exploration.go` / `db/intent_edit.go` | 状态机白名单 + content_version+1 |
| `proposedPendingCols/RejectedReviewFilter` | `db/exploration.go` | 待审/驳回单源过滤器 |
| `approveIntentAt/RequestRevisionAt` | `db/exploration.go` | 审核写 CAS（FOR UPDATE + 版本） |
| `Frontier/HasActiveIntent/HasProposedIntent/HasRejectedIntent` | `db/exploration.go` | 引擎读取门面 |
| `FreshPendingIntents` | `db/exploration.go` | 预审候选（reviewed_version < content_version） |
| `GetAgentByExecutorKey/SetAgentExecutorKey` | `db/config.go` | executor_key 查询/声明 |
| `AgentSkillNames/SetAgentSkillVisibility` | `db/config.go` | skill 可见性 |
| `CheckAssetsIntercept/EvaluateAssetGate` | `db/asset_intercept_match.go` | 资产范围拦截 |
| `CreateTrigger/ListEnabledTriggers/NewToolCallsSince` | `db/triggers.go` | 触发器 CRUD + 工具事件源 SQL |
| `SeedPromptIfEmpty/CurrentPrompt` | `db/config.go` | prompt 播种/取当前 |

### A.4 intercept / guard 包

| 函数 | 位置 | 简介 |
|---|---|---|
| `SkipReviewGateFor` | `intercept/intercept.go` | 审核写工具非 review 来源旁路 |
| `IsToolEnabled` | `intercept/intercept.go` | 启用集闸 |
| `systemBlockMessage` | `guard/guard.go` | deny 消息前缀「ARTEX 平台管控」 |

---

> 文档完。附录索引和正文均以发布快照 commit `1902e43` 的文件与函数/语句锚点为准；后续提交请重新搜索符号，不要沿用旧数字行号。
