# ARTEX Goal 与外网 Planner 立足点提示词实施记录

状态：已实施、已部署。  
变更类型：仅提示词与提示词配置，不增加工具硬校验。  
适用范围：Goal 拆解器；Planner 的“外网渗透”内置模板。  
明确排除：其他五套 Planner 领域模板、Worker、Dispatcher、历史任务提示词快照、任务 #13。

## 1. 背景与目标

任务 #13 曾只有 RCE Finding，却被 Planner 当作系统权限和 getshell 完成条件，随后提前收官。Dispatcher 在缺少 WebShell/C2 立足点时没有派发，这部分行为正确。

本次通过 Goal 和 Planner 提示词实现两个目标：

1. getshell 任务必须理解“实际立足点”验收条件，不能把 RCE、上传成功或本地监听当作 Shell 上线。
2. 普通漏洞检测和验证仍按各自证据完成，不被强制升级为 getshell，不产生误阻。

用户明确要求通过更丰富的关键词让 LLM 自行进行语义匹配，并限定 Planner 只同步外网渗透内置模板。

## 2. 修改文件与定位

| 文件 | 位置 | 变更 |
|---|---|---|
| `agent/goals.go` | `goalsDefaultTmpl`，`调用 set_goals` 前 | 增加目标类型、强弱意图和中英文关键词规则 |
| `agent/planner.go` | `plannerDefaultTmpl` 第 2 步“判目标” | 仅外网 Planner 增加 getshell 证据验收规则 |
| `agent/foothold_prompt_test.go` | 新文件 | 校验规则存在，并确保其他五套领域模板未改变 |
| `config-export/agent_prompts_current.jsonl` | Goal 当前提示词 | 从正式库重新导出，包含新 Goal 规则 |
| 正式库 `planner_templates.id=1` | 外网渗透 | 保留用户已有正文，在第 2 步插入新规则 |
| 正式库 Goal 当前提示词 | `agent_prompts` | 新增版本并切换 `agents.current_prompt_id`，保留历史版本 |

没有修改：

- `agent/planner_domain_templates.go`；
- `agent/tools.go` 的 `proveGoal` / `goalMet`；
- Worker、Dispatcher 和触发器；
- `tasks.planner_prompt_override` 历史快照；
- 数据库表结构。

## 3. Goal 新增提示词

以下文本已加入 `agent/goals.go` 的 `goalsDefaultTmpl`：

```text
**目标类型与 Shell 意图语义匹配（避免误拆、误阻）**：
- 忽略关键词大小写、空格、连字符、下划线和中英文表达差异，结合完整语义判断，不做机械单词匹配；结构化目标类型优先于孤立关键词。
- **强 Shell/立足点意图**：getshell、get shell、shell、interactive shell、reverse shell、bind shell、webshell、web shell、C2、command and control、beacon、meterpreter、session、foothold；拿 Shell、拿主机、拿下主机、控制主机、控制服务器、远程控制、交互控制、交互 Shell、反弹 Shell、反弹会话、绑定 Shell、上传并连接 WebShell、C2 上线、木马上线、会话上线、建立会话、建立/获得立足点、进入内网后继续渗透。上下文明确要求上述结果时，将 Goal 标为 vulnclass=getshell，并写明验收条件：WebShell 已连接并成功执行验证命令，或反弹/C2 会话实际上线且具有交互证据；仅 RCE 回显、文件上传、反序列化或木马投放不算完成。WebShell、反弹 Shell、C2 是可选实现路径，不得拆成必须全部完成的独立目标。
- **弱权限意图**：获取/获得/拿权限、提权、root、administrator、system 权限、最高权限、获取目标权限、特权访问。弱权限词不能单独判定为 getshell；只有上下文同时要求 Shell、交互访问、会话、远程控制或立足点时才归类为 getshell，否则保持权限提升或漏洞验证目标。
- **检测/验证意图**：检测、探测、扫描、排查、验证、确认、复现、漏洞验证、漏洞检测、PoC、proof of concept、是否存在、是否可利用、能否执行命令、验证 RCE、验证上传、验证反序列化。只有检测/验证意图时按实际漏洞分类为 rce、file_upload、deserialization、sqli 等，不擅自升级为 getshell，也不要求继续建立 Shell。例如「验证 RCE」是 rce；「验证 RCE 并建立反弹 Shell」才包含 getshell 目标。
```

### Goal 行为示例

| 用户目标 | 预期 Goal 类型 | 是否要求 Shell |
|---|---|---|
| 检测并验证 S2-045 RCE | `rce` | 否 |
| 验证文件上传漏洞 | `file_upload` | 否 |
| 验证反序列化漏洞 | `deserialization` | 否 |
| 获取 root 权限 | 权限提升或漏洞验证，依上下文 | 不自动要求 |
| 验证 RCE 并建立反弹 Shell | 包含 `getshell` | 是 |
| 获取交互 Shell / 建立立足点 | `getshell` | 是 |

## 4. 外网 Planner 新增提示词

以下文本只加入 `agent/planner.go` 的 `plannerDefaultTmpl`，位置在“量化验收核对”之后、第 3 步之前：

```text
- **Getshell 目标验收与证据语义匹配（仅 vulnclass=getshell）**：
  · 优先使用 Finding 的结构化 vulnclass，关键词只辅助理解证据，不能用孤立关键词覆盖结构化类型。只有 webshell、reverse_shell 或 getshell 类型的确认 Finding，且证据指向目标资产并证明立足点实际可用，才能作为 prove_goal 的立足点证据。
  · WebShell 证据包括：webshell connected、shell_test success、connection successful、command executed、AntSword、Behinder、Godzilla；WebShell 已连接、连接成功、蚁剑/哥斯拉/冰蝎连接、执行命令有回显、whoami/id 返回目标主机结果。反弹/C2 证据包括：session opened、shell session established、reverse connection accepted、beacon online、meterpreter session、C2 online、callback received；反弹/回连成功、会话已建立/上线、Beacon/C2 上线、Meterpreter 会话、目标主机上线、交互命令有回显。攻击机本地 Shell、监听端口启动不算目标上线。
  · RCE、remote code execution、command execution、id/whoami 回显、root 命令回显；文件上传/JSP 写入成功、文件存在、HTTP 200、木马/WebShell 已上传；反序列化成功、权限提升成功、获得 root 权限、监听端口已启动、PoC 执行成功，均不能单独证明 getshell。它们可以完成相应的 rce、file_upload、deserialization、sqli 或权限验证目标，不要求继续建立 Shell。
  · getshell 目标当前只有 RCE、上传或普通事实时，禁止 prove_goal，改派「建立并验证交互立足点」的意图；失败时记录真实受阻原因，不得提前收官或要求 Worker 伪造证据。调用 goal_met 前同样检查是否存在未完成的 getshell 目标，不得绕过验收条件。
```

### 关键词使用优先级

1. 优先使用结构化 `vulnclass`。
2. 再理解完整目标和证据语义。
3. 关键词忽略大小写、空格、连字符、下划线和中英文表达差异。
4. 弱权限词不能单独触发 getshell。
5. 检测/验证词不能把普通漏洞目标升级为 getshell。
6. 攻击机本地 Shell、监听启动和上传成功不能代替目标立足点证据。

## 5. 正式库同步方式

### 5.1 备份

```text
/root/artex-pre-foothold-prompt-20261029-113116.dump
```

备份权限为 `0600`。

### 5.2 外网模板

只对满足以下条件的记录执行正文插入：

```sql
WHERE id = 1
  AND name = '外网渗透'
  AND builtin = true
  AND position('Getshell 目标验收与证据语义匹配' in body) = 0;
```

执行结果：`UPDATE 1`。没有覆盖模板名称、`nkey`、默认标记或用户已有正文。

### 5.3 Goal 当前提示词

没有覆盖旧版本。通过 `INSERT INTO agent_prompts ... version + 1` 新增版本，再更新 `agents.current_prompt_id`。

部署后查询：

```text
外网渗透：has_new_rule=true
漏洞挖掘：false
代码审计：false
内网渗透：false
权限提升：false
漏洞利用：false
Goal 当前提示词 version=2
包含新规则的历史任务快照数量=0
```

## 6. 自动化测试与实现映射

### 6.1 `TestGoalAndExternalPlannerFootholdSemantics`

文件：`agent/foothold_prompt_test.go`

覆盖：

- Goal 包含强 Shell 意图规则；
- 弱权限词不能单独分类为 getshell；
- 检测/验证按实际漏洞分类；
- WebShell、反弹 Shell、C2 是可选路径；
- 外网 Planner 只接受 webshell/reverse_shell/getshell 立足点 Finding；
- 攻击机本地 Shell 和监听启动不算目标上线；
- RCE、上传等不能单独证明 getshell；
- 普通漏洞验证目标不被要求继续建立 Shell；
- `goal_met` 前也要求检查未完成 getshell 目标。

### 6.2 `TestFootholdPromptChangeIsLimitedToExternalPlanner`

遍历 `PlannerDomainSeeds()`，确保以下五套正文均不含本次外网专属标记：

- 内网渗透；
- 权限提升；
- 漏洞利用；
- 漏洞挖掘；
- 代码审计。

### 6.3 全量验证

```bash
ARTEX_PG_DSN=<独立测试库> go test -p=1 ./...
go vet ./...
git diff --check
```

全部通过。日志：

```text
/root/artex-foothold-prompt-go-test.log
/root/artex-foothold-prompt-go-vet.log
```

这些测试验证提示词内容和变更范围，不证明真实 LLM 在所有输入下都会遵守。

## 7. 部署与运行状态

```bash
docker compose up -d --build artex
curl http://127.0.0.1:8787/api/health
```

结果：

```json
{"ok":true,"service":"artex","version":"dev"}
```

`artex-ld-artex-1` 和 `artex-ld-postgres-1` 均为 healthy。

部署日志：

```text
/root/artex-foothold-prompt-deploy.log
```

## 8. 预期运行流程

### 漏洞检测/验证

```text
用户要求验证 RCE
→ Goal=rce
→ 获得可靠命令回显或回连
→ Planner 按 RCE 证据验收
→ 正常完成，不要求 WebShell
```

### Getshell

```text
用户要求交互 Shell
→ Goal=getshell，并写入立足点验收条件
→ 只有 RCE/上传时继续规划建立并验证立足点
→ WebShell 连接并执行命令，或 C2/反弹会话上线
→ 登记 webshell/reverse_shell/getshell Finding
→ Planner 再评估 prove_goal
```

## 9. 边界与未验证项

1. 本次是提示词约束，不是代码硬校验；模型仍可能不遵守。
2. `prove_goal` 和 `goal_met` 的工具实现没有新增 vulnclass 检查。
3. 其他五套 Planner 领域模板没有本次新规则，这是用户明确限定。
4. 历史任务快照没有修改，旧任务不保证获得新行为。
5. 任务 #13 没有重开、重跑或修改。
6. 未进行真实目标、WebShell、C2 或 LLM 端到端测试。
7. 如果之后仍出现“只有 RCE 却完成 getshell”，需要另立任务增加 `prove_goal` / `goal_met` 硬校验。
