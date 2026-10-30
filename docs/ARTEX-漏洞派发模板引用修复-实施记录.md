# ARTEX 漏洞派发模板引用修复实施记录

> 状态：代码、测试和 Docker 部署已完成；尚未提交 Git。
> 范围：`vuln_dispatcher`、`spawn_task` 模板自然键引用、配置导入与存量提示词迁移。

## 1. 基线与问题

- 仓库：`/root/artex-ld`
- 基线提交：`bd38530`
- 关联方案：`ARTEX-漏洞派发模板引用修复方案.md`
- 原因：导入的提示词保存 `planner_template_id=7/8/9/10/11`，当前数据库实际场景 ID 为 `4/5/6/2/3`。数字 ID 是环境相关主键，跨环境导入后会失效。

## 2. 变更总表

| 编号 | 类型 | 文件或对象 | 具体变化 | 验证 |
|---|---|---|---|---|
| DISP-001 | 修改 | `server/orchestration.go` | `spawn_task` 新增 `planner_template_nkey`，按 nkey 查询并快照模板正文 | 针对性 Go 测试通过 |
| DISP-002 | 修改 | `server/orchestration.go` | 数字 ID 与 nkey 互斥；模板不存在时返回包含引用的明确错误 | 互斥和不存在测试通过 |
| DISP-003 | 修改 | `agent/promptcatalog.go` | 五个场景改用中文稳定 nkey，不再保存数字 ID | 默认提示词测试通过 |
| DISP-004 | 配置 | `config-export/agent_prompts_current.jsonl` | 导入配置同步改为 nkey | 配置差异检查通过 |
| DISP-005 | 数据库 | `settings` / Agent 当前提示词 | 新增一次性迁移标记 `vuln_dispatcher_v6_template_nkey`；仅迁移默认血统提示词 | 已部署核验 |
| DISP-006 | 测试 | `server/vuln_dispatcher_nkey_test.go` | 新增四个单元/数据库测试 | 全部通过 |
| DISP-007 | 文档 | 本实施记录 | 记录代码、配置、兼容性和验证证据 | 本文 |

## 3. 代码变更明细

### 3.1 `server/orchestration.go`

修改工具构造函数：

```go
func (s *Server) toolSpawnTask() actool.CoreTool
```

新增工具参数：

```text
planner_template_nkey: string
```

参数和错误语义：

1. `planner_template_nkey` 去除首尾空白后按 `planner_templates.nkey` 精确查询。
2. `planner_template_id` 与 `planner_template_nkey` 同时传入时返回“互斥”错误，避免两个引用不一致。
3. 只传 nkey 时调用 `GetPlannerTemplateByNKey`；只传数字 ID 时仍调用 `GetPlannerTemplate`。
4. 数据库查询失败与模板不存在分别报错；不存在的错误包含调用方实际传入的 nkey 或 ID。
5. 查询成功后仍执行原有 `validatePlannerTemplateBody`，并将正文快照到子任务；后续修改或删除模板不改变已建任务。
6. `planner_prompt` 的优先级保持不变。

新增存量迁移函数：

```go
func (s *Server) upgradeVulnDispatcherNKey()
```

只处理同时具有“漏洞派发 agent”“审核开关由人决定”“去重的键是”默认血统特征、且仍含旧模板引用的提示词。自定义血统不覆盖。旧占位符转换保留在：

```go
func (s *Server) resolveVulnDispatcherPrompt(tmpl string) string
```

### 3.2 `agent/promptcatalog.go`

修改常量：

```go
const VulnDispatcherDefaultPrompt
```

五个场景现在分别使用：

```text
内网渗透   → planner_template_nkey=内网渗透
权限提升   → planner_template_nkey=权限提升
漏洞利用   → planner_template_nkey=漏洞利用
漏洞挖掘   → planner_template_nkey=漏洞挖掘
代码审计   → planner_template_nkey=代码审计
```

提示词不再包含 `planner_template_id=【TPL_ID_*】`，也不再指示模型在 ID 为 0 时省略模板。

### 3.3 配置与数据迁移

- `config-export/agent_prompts_current.jsonl` 中 `vuln_dispatcher` 的五个数字 ID 全部替换为场景 nkey。
- 不修改 `planner_templates` 表结构；复用已有唯一 `nkey`。
- 不修改 Agent 工具绑定、触发器、LLM 配置或凭据。
- 部署时一次性更新默认血统的当前提示词，并写入 settings 迁移标记。
- 旧数字 ID 工具调用保持兼容，但新配置不再生成这种调用。

## 4. 新增测试

文件：`server/vuln_dispatcher_nkey_test.go`

| 测试 | 覆盖内容 |
|---|---|
| `TestVulnDispatcherDefaultPromptUsesStableTemplateKeys` | 五个场景均使用 nkey，且无数字参数或旧占位符 |
| `TestResolveVulnDispatcherLegacyPlaceholders` | 旧占位符可转换为稳定场景名 |
| `TestSpawnTaskRejectsAmbiguousTemplateReference` | ID 与 nkey 同传时失败 |
| `TestSpawnTaskRejectsUnknownTemplateNKey` | 使用真实 PostgreSQL 验证不存在模板的错误语义 |

执行结果：

```text
go test ./server -run 'Test(VulnDispatcher|ResolveVulnDispatcher|SpawnTask)' -count=1 -v
PASS
ok github.com/Autumn-27/artex/server
```

测试数据库为独立 `artex-test-pg`，仅监听 `127.0.0.1:55432`，不使用正式数据库。

## 5. 部署核验与回滚

已核验：容器日志存在 nkey 迁移记录；数据库当前提示词包含五个场景 nkey，旧数字 ID 为 0；容器健康检查通过。

回滚时恢复本轮前数据库备份和旧镜像即可。旧镜像不认识 `planner_template_nkey`，因此回滚前必须同时恢复旧提示词；不能只回滚代码而保留新提示词。

## 6. 边界

本次没有实现真实 LLM 派发端到端测试，也没有对真实目标执行渗透。验证范围是工具参数、模板查询、提示词迁移和错误语义。统一全量测试、浏览器回归和 Docker 部署结果在发布阶段补充。

## 最终验证补充

- 部署后当前提示词包含五个 nkey，旧硬编码模板 ID 为 0；启动日志显示一次性迁移完成。
- 全新独立 PostgreSQL 上全量 Go 测试和 vet 通过。

## 7. 代码级补充：参数解析与快照

以下是 `server/orchestration.go` 实际采用的关键逻辑（省略无关日志）：

```go
plannerNKey := strings.TrimSpace(a.PlannerTemplateNKey)
if plannerNKey != "" && a.PlannerTemplateID != 0 {
    return actool.Errorf("planner_template_id 与 planner_template_nkey 互斥，请只传一个"), nil
}
if plannerPrompt == "" && (a.PlannerTemplateID > 0 || plannerNKey != "") {
    var tpl *db.PlannerTemplate
    var tplErr error
    reference := fmt.Sprintf("planner_template_id #%d", a.PlannerTemplateID)
    if plannerNKey != "" {
        tpl, tplErr = s.m.PG().GetPlannerTemplateByNKey(plannerNKey)
        reference = fmt.Sprintf("planner_template_nkey %q", plannerNKey)
    } else {
        tpl, tplErr = s.m.PG().GetPlannerTemplate(a.PlannerTemplateID)
    }
    if tplErr != nil {
        return actool.Errorf(fmt.Sprintf("读取 planner 模板失败：%v", tplErr)), nil
    }
    if tpl == nil {
        return actool.Errorf(fmt.Sprintf("%s 不存在；可用模板：%s",
            reference, s.plannerTemplateList())), nil
    }
    if err := validatePlannerTemplateBody(tpl.Body); err != nil {
        return actool.Errorf(fmt.Sprintf("模板 #%d 渲染校验失败：%s", tpl.ID, err)), nil
    }
    plannerPrompt = tpl.Body
}
```

重要语义：`planner_prompt` 仍优先；模板引用只在创建时取正文；不会把 nkey 留到运行时再查询。

## 8. 代码级补充：存量提示词迁移

`server/orchestration.go:1081` 的实际迁移：

```go
func (s *Server) upgradeVulnDispatcherNKey() {
    const flag = "vuln_dispatcher_v6_template_nkey"
    if v, _, _ := s.m.pg.GetSetting(flag); v == "true" { return }
    defer func() { _ = s.m.pg.SetSetting(flag, "true") }()
    a, err := s.m.pg.GetAgentByKey("vuln_dispatcher")
    if err != nil || a == nil { return }
    cur, err := s.m.pg.CurrentPrompt(a.ID)
    if err != nil || (!strings.Contains(cur, "planner_template_id") &&
        !strings.Contains(cur, "【TPL_ID_")) { return }
    if !strings.Contains(cur, "漏洞派发 agent") ||
        !strings.Contains(cur, "审核开关由人决定") ||
        !strings.Contains(cur, "去重的键是") {
        log.Printf("[vuln_dispatcher] 提示词含自定义修改，跳过 nkey 自动迁移")
        return
    }
    if _, err := s.m.pg.ResetPromptToDefault(a.ID, agent.VulnDispatcherDefaultPrompt); err != nil {
        log.Printf("[vuln_dispatcher] nkey 提示词迁移失败: %v", err)
        return
    }
    log.Printf("[vuln_dispatcher] 场景模板已迁移到稳定 nkey")
}
```

这解释了为什么不覆盖自定义提示词：只有三个默认血统特征同时存在才重置。

## 9. 提示词 Before/After 原文

修改前（每种场景写死环境相关 ID）：

```text
已拿主机立足点 → 内网渗透（planner_template_id=7）
立足点但权限低 → 权限提升（planner_template_id=8）
明确 CVE/武器化路径 → 漏洞利用（planner_template_id=9）
同族漏洞面未探明 → 漏洞挖掘（planner_template_id=10）
源码/仓库可获得 → 代码审计（planner_template_id=11）
模板 id 为 0 则省略 planner_template_id 参数。
```

修改后（`agent/promptcatalog.go` 和 `config-export/agent_prompts_current.jsonl` 同步）：

```text
## 场景映射（根据 vulnclass 与漏洞描述的语义选择子任务场景，spawn_task 传对应 planner_template_nkey）
- 已拿主机立足点 → 内网渗透（横向移动，planner_template_nkey=内网渗透）。
- 立足点但权限低 → 权限提升（planner_template_nkey=权限提升）。
- 有明确 CVE 编号或武器化利用路径、尚未拿权限 → 漏洞利用（planner_template_nkey=漏洞利用）。
- 同族漏洞面未探明、需要扩大挖掘范围 → 漏洞挖掘（planner_template_nkey=漏洞挖掘）。
- 描述显示源码/仓库/代码泄露可获得 → 代码审计（planner_template_nkey=代码审计）。
planner_template_nkey 必须原样使用上述场景名；不要猜测、换算或硬编码数字模板 ID。
```

## 10. 回滚的数据一致性

代码和提示词必须一起回滚。旧代码不认识 `planner_template_nkey`，如果只回滚镜像，Dispatcher 会继续发送新参数并失败。正确顺序：

1. 备份当前 `agent_prompts_current` 和 `settings`。
2. 恢复旧镜像。
3. 将 `vuln_dispatcher` 当前提示词恢复为旧版本，或恢复部署前数据库备份。
4. 重启后检查五个场景映射和 `/api/health`。

## 11. 测试与实现代码逐行对照

| 测试 | 测试文件和行号 | 对应实现 | 覆盖点 |
|---|---|---|---|
| `TestVulnDispatcherDefaultPromptUsesStableTemplateKeys` | `server/vuln_dispatcher_nkey_test.go:13` | `agent/promptcatalog.go:81` `VulnDispatcherDefaultPrompt`；映射在 `:98-107` | 默认提示词使用五个 nkey，不包含失效数字 ID。 |
| `TestResolveVulnDispatcherLegacyPlaceholders` | `server/vuln_dispatcher_nkey_test.go:25` | `server/orchestration.go:991` `resolveVulnDispatcherPrompt` | 旧 `【TPL_ID_...】` 占位符被替换成稳定 nkey。 |
| `TestSpawnTaskRejectsAmbiguousTemplateReference` | `server/vuln_dispatcher_nkey_test.go:33` | `server/orchestration.go:243` `toolSpawnTask`；互斥检查在 `:307` | 同传数字 ID 和 nkey 必须明确失败。 |
| `TestSpawnTaskRejectsUnknownTemplateNKey` | `server/vuln_dispatcher_nkey_test.go:46` | `server/orchestration.go:313-324` | 不存在的 nkey 在创建子任务前被拒绝；使用真实 PostgreSQL 验证。 |
| 已导入旧提示词升级 | 启动后数据库核验 | `server/orchestration.go:1081` `upgradeVulnDispatcherNKey` | 默认血统提示词升级为 nkey；设置项保证一次性，不覆盖自定义分叉。 |

执行命令：

```bash
ARTEX_PG_DSN=... go test ./server -run 'TestVulnDispatcher|TestResolveVulnDispatcher|TestSpawnTask' -v
```
