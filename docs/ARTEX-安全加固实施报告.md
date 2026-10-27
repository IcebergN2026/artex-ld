# ARTEX 安全加固实施报告

> 依据 `ARTEX-安全代码审计报告.md`（reviewer 全仓审计）分期实施。
> 本报告记录已完成的 Phase 0（配置）与 Phase 1（代码）及其实测验收。
> 发布代码基线：`1902e43` ｜ 本文记录安全加固设计与验证，不包含内部实例信息。
>
> **发布口径**：这是特定实例的历史实施记录，不等于代码默认值。源码默认判官关闭、`fail_action=allow`，默认拦截启用集也不含 `ExecuteExtraTool`；文中的 deny + 14 项是手工配置。`数据外泄管道` 的规则种子与通用正则匹配器已在发布快照中，但规则默认关闭，且只覆盖一种命令管道形态，不能作为通用外泄防护。

---

## 总览

| 期       | 范围                                             | 状态         |
| ------- | ---------------------------------------------- | ---------- |
| Phase 0 | 拦截/判官五条配置（纯 SQL，无需重启）                          | ✅ 已落库并实测   |
| Phase 1 | 三个代码级 🔴 修复                                    | ✅ 已提交部署并实测 |
| Phase 2 | MCP 允许列表 / Bash 危险 ask 扩充 / SSRF 统一守卫          | ⬜ 待实施      |
| Phase 3 | 容器非 root / proxy 绑 127.0.0.1 / 登录限速+改密吊销 / TLS | ⬜ 待实施      |

---

## Phase 0 — 判官与拦截配置（commit 无代码变更，纯配置）

### 背景机制
- 工具拦截链：`guard.applyIntercept`（guard/guard.go）→ 启用集检查 → 正则规则库 → **规则全不命中则落 LLM 判官**（intercept/intercept.go `Judge()`）。
- 判官默认关闭、且失败策略默认 `allow`（fail-open），等于"规则外的调用默认全放行"。

### 落库配置（单事务，回滚=关①）
```sql
llm_judge_enabled            = true        -- ① 判官总开关
llm_judge_fail_action        = deny        -- ② 判官挂/超时/输出乱 → 拒（fail-closed，原 allow 是洞）
llm_judge_ask_timeout_action = deny        --    判官转人工后 300s 不处理 → 拒（代码默认即 deny）
llm_judge_profile_id         = <判官 LLM 档 id>  -- ③ 判官模型档（便宜快即可；实例级自增 id，换库失效）
                                          --    取值：SELECT id,name FROM llm_profiles
intercept_enabled_tools      = [...14 项含 ExecuteExtraTool]  -- ④ MCP 调用纳审（整体替换语义）
intercept_rules[数据外泄管道] enabled=true  -- ⑤ 历史实例开启规则记录；规则种子/匹配器随快照发布，默认 disabled
```

### 实测验收
```
intercept_pending 最新记录：
  Bash|denied|rule|禁止删除系统关键路径          ← 规则库硬拦
  Bash|denied|rule|破坏性命令被拒绝（rm -rf…）   ← 规则库硬拦
  Bash|allowed|model|[模型] 实际操作：在工作目录写入 recon.jsp… ← 判官裁决生效 ✓
```
判官泳道（`decision_source=model`）出现真实裁决 → ①~④ 全部生效。

### 生效后的行为变化
- 启用集内每次工具调用多一次判官 LLM 往返（约 1-3s）→ 打靶速度变慢，是安全的价格；
- 判官不可用（LLM 断线）时启用集工具被拒（fail-closed）→ 任务卡住报"模型审批失败"；
- 用量页新增 "judge" 泳道可监控判官 token 开销。

---

## Phase 1 — 三个代码级修复（已包含在发布快照 `1902e43`）

### ① POST /api/auth/init 抢注 → setup token 首启令牌（后续已移除）
> **当前版本变更**：应部署要求，首次安装令牌机制已完整移除；后端不再生成或校验令牌，初始化页面无需填写令牌。以下仅保留原加固方案的历史记录，不代表当前行为。

**原漏洞**：无 setup token，任何先到者对全新实例 POST 一次即设密码接管（公网部署致命）。
**原修复**：
- `server/auth.go`：启动时若未设密码 → 生成 32 字符随机令牌写 `dataDir/setup-token`（0600）并打印日志；init 必须携带 `setup_token`（`subtle.ConstantTimeCompare` 比对）；设密成功即删文件、令牌作废；令牌读不到 → fail-closed 503。
- `web/src/app/(auth)/setup/page.tsx`：setup 页加"安装令牌"输入框 + 获取指引。
- `web/src/lib/api.ts`：`initPassword(password, setupToken)`。
**实测**：已初始化实例 `POST /api/auth/init` → `403 密码已设置` ✓；`/opt/artex/data/setup-token` 已按设计清除 ✓。
**测试**：`TestAuthInitRejectsWithoutSetupToken`（无/错/对令牌 + 已初始化 4 用例）、`TestEnsureSetupToken`（生成/复用/0600）。

### ② 文件工具不限目录 → Root 路径硬边界
**漏洞**：Read/Write/Edit/MultiEdit/LS/Glob/Grep 接受绝对路径，注入可诱导 agent 写 `~/.ssh/authorized_keys`、读 `jwt.key`——提示词"别写外面"拦不住（软防线）。
**修复**（结构性硬防线，代码层）：
- `tool.ToolContext` / `harness.QueryInput` / `agentcore.Options` 新增 `Root` 字段；
- `resolvePath`（third_party/norma/tool/helpers.go）集中钳制：解析结果出 Root 树即改写为不存在 sentinel → os 调用必然 ENOENT；
- 五个常规 agent 装配点（worker/chat/planner/mainagent/customexec）全部挂 `Root = workDir`。这里说的是文件工具的五个常规装配点；其他 `ModeBypass` 执行路径是否接触文件工具需单独审查，不能由此推导出所有旁路都有同一 Root 边界。
**实测**：二进制含 `escape-denied` sentinel ✓；`TestResolvePathRootConfinement`（6 路径用例）+ `TestReadToolRejectsOutsideRoot` ✓。
**注意**：Windows 下 `/etc/passwd` 非绝对路径（POSIX 语义），`..` 逃逸与盘符绝对路径在 Windows 均被正确钳制；生产 Linux 下语义完整。

### ③ python_interpreter 无校验 → 解释器白名单校验
**漏洞**：`PUT /api/settings {"python_interpreter":"/bin/sh"}`（需 JWT）→ 之后任何 script 型自定义工具被当 shell 跑 = RCE 门槛骤降。
**修复**：
- `server/customtool.go` 新增 `validatePythonInterp`：必须绝对路径 + 存在可执行 + basename ∈ {python, python3, python3.x, pypy…}；
- `PUT settings` 入口校验 400；**存量脏值在使用侧（`pythonInterpreter()`）同样拦**（忽略+warn 日志）——防御已入库的旧脏数据。
**实测**：`PUT /bin/sh` → `400 python_interpreter 不合法: …当前: sh` ✓；`PUT /usr/bin/python3` → 200 ✓。

### 回归测试
```
go test ./server/ -run 'TestAuthInit|TestAuthStatus|TestValidatePassword'  ok
go test norma/tool -run Root|ResolvePath|ReadTool  3 PASS
go build ./... && go vet ./server/ ./agent/   全绿
（server 包既有 TestExecPython 因本机无 python 跳过，与本次无关）
```

---

## 关联修复（本次会话顺带）

| commit | 内容 | 在 `1902e43` 中 |
|---|---|---|
| `168e6d1` | 触发运行携带 taskID → 自定义 agent（trace_cleaner）可用探索图工具，record_fact 销账落账 | ✅ 已包含 |
| `e7e6ed2` | 修 168e6d1 副作用：会话型旁路父引用同带 ConversationID+TaskID 撞 side_question_sessions CHECK（23514 刷 20842 行日志）→ 会话型优先清任务字段 | ✅ 已包含 |
| `c7e33f0` | 预审 provider 兜底改 ActiveProfile（修 206 无人预审）+ toolGetTaskGraph 追加 child_tasks（修派发去重无数据→重复派生 207） | ✅ 已包含 |

---

## 剩余风险与 Phase 2/3 建议

1. **判官是 LLM 软防线**：混淆编码可骗过——不能是唯一一层，Phase 1 的代码边界才是铁闸；
2. **Phase 2**（建议下个窗口）：MCP stdio 允许列表（现 `curl/socat/tee` 作 command 放行）、Bash 危险模式 ask 扩充（`curl|sh`、`base64 -d|sh`）、SSRF 统一守卫（blockInternalDial 抽公共，WebFetch/customtool/通知 webhook 共用）；
3. **Phase 3**：容器非 root、proxy 绑 127.0.0.1、登录限速+改密吊销旧 token、可选 TLS；
4. **遗留观察项**（与本次无关）：planner 目标达成判定偏宽松（仅 RCE 证明即判"获取 shell"）、spawn_task 约束误拦（child_goal 引用父禁令原文触发 ruleMatches 假阳性）——均在提示词/规则层，可后续单独处理。

---

## 回滚指引
- Phase 0：`UPDATE settings SET value='false' WHERE key='llm_judge_enabled'`（其余键留着无害）；
  若想连**扩大的管控面**一起回退，**或恢复原 `intercept_enabled_tools` 启用集**——只关 ① 会让 14 项
  启用集仍然生效（判官已关＝规则外全放行，而每次调用白付 DB 往返），恢复原启用集即可回到改动前口径；
- Phase 1：在完整开发历史中回滚对应加固提交后重新构建；单提交发布包没有内部历史，不能直接执行旧的内部提交号。当前版本已移除首启令牌，不应按上述历史记录恢复令牌流程。
