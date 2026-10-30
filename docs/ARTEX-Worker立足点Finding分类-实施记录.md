# ARTEX Worker 立足点 Finding 分类：实施记录

状态：已实施并部署（2026-10-29）
对应需求：Worker 确认 getshell、上传 Webshell 或获取目标权限后，用明确的 vulnclass 登记，不再统一写成 getshell。

## 1. 最终行为

| 已验证结果 | report_finding.vulnclass |
|---|---|
| 反弹 Shell、命令 Shell 等主机命令执行权限 | `getshell` |
| Webshell 已上传且可访问或连接控制 | `webshell` |
| 已获得目标系统或应用权限，但不属于更具体的 getshell/webshell，例如账户控制、登录成功、权限提升 | `获取目标权限` |

不新增 `foothold`，不修改 severity，不新增数据库字段。vulnclass 仍是字符串；三个值分别填写，不把 `getshell/webshell/获取目标权限` 整体写成一个值。

保留证据门槛：只有实际验证成功才能 report_finding；仅上传但无法访问或连接、理论可利用、版本/CVE 推断，仍只登记 inferred 事实。

## 2. 新增代码

### `agent/worker_prompt_test.go`

新增 `TestWorkerPromptClassifiesVerifiedFootholds`，检查 Worker 默认提示词包含：

- getshell 的独立定义；
- webshell 的独立定义；
- 获取目标权限的独立定义；
- Webshell 实连成功后按 webshell 登记的要求。

## 3. 修改代码与配置

### `agent/worker.go`

修改 `workerDefaultTmpl`：

1. webshell 纪律由“有回显才按 getshell”改为“有回显才按 webshell”；另有反弹 Shell 等主机命令执行证据时才按 getshell。
2. vulnclass 归类规范新增 webshell、获取目标权限两个值，并缩小 getshell 的定义，消除 Webshell 与 getshell 的混用。
3. “九类都不符”改为“十一类都不符”。
4. 严重证据要求覆盖 getshell/webshell/获取目标权限/rce/file_upload/deserialization。

### `config-export/agent_prompts_current.jsonl`

同步 Worker 当前提示词，避免已有部署因数据库 PromptOverride 而继续使用旧定义。

### `config-export/agent_triggers.jsonl`

同步 trace_cleaner 的 finding_message：

```text
vulnclass ∈ {getshell,webshell,获取目标权限,rce} 且 severity ∈ {critical,high}
```

满足条件才进入清理审计。这里仍遵守已确定的纯对话人工确认方案，不是收到 Finding 后直接删除。

## 4. 数据库与部署

没有表结构或 API 变更。

在正式 PostgreSQL 中仅更新：

- agents.key=worker 当前 agent_prompts 正文；
- agent_triggers.agent_key=trace_cleaner 的 finding_message。

未全量导入 config-export，未覆盖其他自定义提示词。

重新构建并启动 artex。数据库核验：

```text
worker_getshell=true, worker_webshell=true, worker_access=true
trace_new_classes=true
```

健康检查返回：

```json
{"ok":true,"service":"artex","version":"dev"}
```

部署日志：`/root/artex-worker-classify-deploy-20261029.log`。

## 5. 验证

- `go test ./agent -run TestWorkerPromptClassifiesVerifiedFootholds -count=1 -v`：通过。
- `go test ./agent -count=1`：通过。
- `git diff --check`：通过。
- 正式数据库三个 Worker 分类值及 trace_cleaner 新条件：全部为 true。
- Docker artex：healthy；/api/health：通过。

## 6. 未验证与边界

- 本次是提示词和触发配置修改，没有连接真实 LLM 执行 getshell/Webshell 渗透流程。
- vulnclass 没有新增后端枚举校验；提示词负责指导分类，Finding 字符串字段和动态筛选可以直接承载这些值。
- vuln_dispatcher 的既有派发规则未在本次需求中扩展；本次只同步 Worker 与 trace_cleaner。
- 清理仍需当前对话中的真实用户确认；提示词不是代码级防误删安全边界。

## 7. 提示词 Before/After 原文

### 7.1 Webshell 纪律

修改前的关键句：

```text
落地后用 mcp__godzilla__shell_add 登记、shell_test 实连，有回显才按 getshell 记 finding。
```

修改后（`agent/worker.go` 的 `workerDefaultTmpl`）：

```text
**webshell 纪律**：涉及 webshell 落地时，shell 一律用 mcp__godzilla__shell_create 生成（延迟加载工具：先 SearchExtraTools 搜 "godzilla" 再 ExecuteExtraTool 调用，传参带上目标语言/加密器/过滤特征），禁止手写硬编码。生成的 shell 写到本意图工作目录再走上传链；落地后用 mcp__godzilla__shell_add 登记、shell_test 实连，有回显才按 webshell 记 finding；若另有反弹 shell 等主机命令执行证据，则按 getshell 登记。MCP 不可用就记 fact 交规划者，不得退回手写。
```

### 7.2 vulnclass 归类规范

修改后的完整分类段：

```text
**vulnclass 归类规范**（vulnclass 决定漏洞是否自动派发深度子任务，务必对号入座、不要随手写 other）：
getshell=已通过反弹 shell、命令 shell 等获得主机命令执行权限；
webshell=Webshell 已上传且可访问或连接控制；
获取目标权限=已获得目标系统或应用权限（如账户控制、登录成功、权限提升），且不属于更具体的 getshell/webshell；
rce=可在目标执行任意命令或代码（含表达式/模板/命令注入实证）；
file_upload=任意文件上传且落地后可访问解析；
deserialization=反序列化利用链实证；
sqli=注入实证（报错/布尔/延时回显其一）；
ssrf=目标服务器发出非预期请求的实证；
logic_flaw=越权/支付/验证码绕过等业务逻辑实证；
info_leak=实际拿到敏感数据内容（不是"可能存在"）；
unauthorized_api=未授权接口实际调用成功且返回敏感数据；
十一类都不符才写 other。
**立足点与严重档分类（getshell/webshell/获取目标权限/rce/file_upload/deserialization）必须有「已执行/已控制」级证据**——只有理论可利用路径的，按上面规则记 inferred 事实而不是 finding。
```

在 Go 源码中以上是一段连续提示词；这里仅为文档可读性换行，没有增加新的分类。

## 8. trace_cleaner 触发消息完整原文

`config-export/agent_triggers.jsonl` 的 `finding_message`：

```text
上面刚有漏洞被 report_finding 登记（上下文给了 [vulnclass/severity] 与摘要）。按你的清理纪律处理：仅 vulnclass ∈ {getshell,webshell,获取目标权限,rce} 且 severity ∈ {critical,high}（=已获得权限/立足点）才进入清理审计；先用 list_task_findings 确认、用 godzilla shell_test 验证立足点活性，再用 get_task_graph 收集该任务全部「[投放]」事实逐项清理；其余一律不清，直接结束。同一主机已有「[清理]」汇总事实的不重复清理。
```

注意：触发消息中的“逐项清理”仍受 trace_cleaner 当前提示词限制，必须先在对话列计划并获得真实用户明确同意。

## 9. 测试代码

`agent/worker_prompt_test.go` 的实际核心断言：

```go
func TestWorkerPromptClassifiesVerifiedFootholds(t *testing.T) {
    for _, want := range []string{
        "getshell=已通过反弹 shell",
        "webshell=Webshell 已上传且可访问或连接控制",
        "获取目标权限=已获得目标系统或应用权限",
        "有回显才按 webshell 记 finding",
    } {
        if !strings.Contains(workerDefaultTmpl, want) {
            t.Fatalf("worker prompt missing %q", want)
        }
    }
}
```

该测试防止以后把 Webshell 又合并回 getshell，或遗漏中文权限分类。

## 10. 数据更新与回滚细节

没有执行全量配置导入。只更新两类记录：

```text
agents.key=worker 对应当前提示词
agent_triggers.agent_key=trace_cleaner 的 finding_message
```

回滚时必须同时恢复 Worker 提示词和 trace_cleaner 触发消息。只回滚其中一个会造成“Worker 已登记新分类，但清理触发器不识别”或相反的不一致。

回滚不需要删除任何 finding，也不需要修改 vulnclass 列；既有字符串值继续保留。

## 10. 测试与实现代码逐行对照

你指出的 `TestWorkerPromptClassifiesVerifiedFootholds` 对应关系如下。行号是本次实施完成时的实际行号。

| 测试或验证 | 测试位置 | 被测实现位置 | 具体检查 |
|---|---|---|---|
| `TestWorkerPromptClassifiesVerifiedFootholds` | `agent/worker_prompt_test.go:8-19` | `agent/worker.go:258`，`workerDefaultTmpl` 的 `vulnclass 归类规范` | 直接读取生产变量 `workerDefaultTmpl`，断言 getshell、webshell、获取目标权限三个定义和“有回显才按 webshell”都存在。不是另写一份测试提示词。 |
| godzilla 纪律静态核验 | 同上测试中的 `有回显才按 webshell 记 finding` | `agent/worker.go:253` | 生产提示词要求 `shell_create` 生成、`shell_add` 登记、`shell_test` 实连；MCP 不可用只记 fact。 |
| 清理触发配置核验 | 部署后数据库查询 | `config-export/agent_triggers.jsonl:3` | trace_cleaner 的 finding_message 包含 getshell、webshell、获取目标权限、rce 和 critical/high 条件。 |
| Worker 导出提示词核验 | 配置导入/单条同步 | `config-export/agent_prompts_current.jsonl:4` | 与 `workerDefaultTmpl` 使用相同分类和证据规则。 |

测试源代码本身位于：

```text
/root/artex-ld/agent/worker_prompt_test.go
```

生产代码不是测试文件，而是：

```text
/root/artex-ld/agent/worker.go
```

执行命令：

```bash
go test ./agent -run TestWorkerPromptClassifiesVerifiedFootholds -v
```

边界：这是提示词内容回归测试，不证明真实 LLM 一定遵守。真实模型分类和 godzilla 实连仍需授权环境验收，本文没有把它写成已通过。
