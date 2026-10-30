# ARTEX 漏洞派发模板引用修复方案

> 状态：设计稿，尚未实施。
> 对象：`vuln_dispatcher`、`spawn_task`、Planner 模板选择及配置导入导出。

## 1. 问题

当前导入的 `vuln_dispatcher` 提示词使用硬编码 `planner_template_id=7/8/9/10/11`，而当前数据库实际模板为：

| 场景 | 当前提示词 ID | 当前数据库 ID | 稳定 `nkey` |
|---|---:|---:|---|
| 内网渗透 | 7 | 4 | `内网渗透` |
| 权限提升 | 8 | 5 | `权限提升` |
| 漏洞利用 | 9 | 6 | `漏洞利用` |
| 漏洞挖掘 | 10 | 2 | `漏洞挖掘` |
| 代码审计 | 11 | 3 | `代码审计` |

`spawn_task` 会检查模板 ID，不存在时拒绝创建任务。因此这是确定性功能故障，不是模型理解问题。

首启代码原本会把 `【TPL_ID_*】` 替换成本环境 ID，但配置导入保存的是另一环境已经展开的数值。数值再次导入后失去可移植性。

## 2. 方案选择

采用：**`spawn_task` 新增 `planner_template_nkey`，Dispatcher 只传稳定自然键，由服务端解析成本环境 ID。**

不采用以下方案作为长期修复：

- 只把当前 `7–11` 改成 `4/5/6/2/3`：只能修复本机，下一次跨环境导入仍会坏。
- 在导出时保留数字 ID：BIGSERIAL 主键本身不具有跨环境身份语义。
- 查不到模板时改用 ID `0` 或默认模板：会把明确场景静默派成错误任务，必须失败关闭。
- 每次启动覆盖当前提示词：会破坏用户自定义内容。

`planner_template_id` 暂时保留，兼容现有 API 和其他调用方；`vuln_dispatcher` 不再使用它。

## 3. 目标流程

```text
vuln_dispatcher
  → spawn_task(planner_template_nkey="内网渗透", ...)
  → 服务端按 nkey 查询 planner_templates
  → 模板不存在：返回错误，不创建任务
  → 模板存在：取得本环境 id 和 body
  → 沿用现有模板渲染校验与正文快照
  → 沿用现有分类默认审核规则
  → 创建子任务
```

关键点：**nkey 解析、模板正文快照和分类默认审核必须使用同一个模板记录。** 不能先按一个 ID 做审核判断，再按另一个引用生成任务。

## 4. 代码改动范围

### 4.1 `server/orchestration.go`

在 `toolSpawnTask` 的参数 schema 和解码结构体中增加：

```json
{
  "planner_template_nkey": "内网渗透"
}
```

处理规则：

1. `planner_template_id` 与 `planner_template_nkey` 同时提供时返回参数错误，避免歧义。
2. 仅提供 `nkey` 时，通过 `GetPlannerTemplateByNKey` 查询。
3. 查询失败或模板不存在时返回明确错误，并列出可用 `name/nkey`，不创建任务。
4. 查询成功后复用现有 `validatePlannerTemplateBody`、正文快照和创建逻辑。
5. 后续 `review_enabled` 的分类默认判断使用解析出的同一个模板。
6. `planner_prompt` 仍为最高优先级；但若显式 `planner_prompt` 与模板引用同传，应返回歧义错误，不再静默忽略模板参数。

将模板选择收敛成一段共享逻辑，ID 和 nkey 两个入口只在解析方式上不同，不复制创建流程。

### 4.2 `agent/promptcatalog.go`

把默认 Dispatcher 提示词改为：

```text
内网渗透 → planner_template_nkey="内网渗透"
权限提升 → planner_template_nkey="权限提升"
漏洞利用 → planner_template_nkey="漏洞利用"
漏洞挖掘 → planner_template_nkey="漏洞挖掘"
代码审计 → planner_template_nkey="代码审计"
```

同时：

- 把“`spawn_task` 传 `planner_template_id`”改为传 `planner_template_nkey`。
- 删除“ID 为 0 时省略参数”的回退说明。
- 明确“nkey 不存在时不得改派默认模板，应结束并报告配置缺失”。
- 删除不再需要的 `【TPL_ID_*】` 占位符和 seed 时数字替换依赖。

授权文案另行按安全审查修正，不与本次模板引用修复混为一个行为变更。

### 4.3 首启和升级逻辑

`seedVulnDispatcherAgent` 直接保存含 nkey 的默认提示词，不再展开数字 ID。

现有 `resolveVulnDispatcherPrompt` 和相关占位符升级逻辑在新流程不再需要时删除，避免两条模板解析链并存。

已有数据库：

- 不按“包含某句话”覆盖用户当前提示词。
- 启动时检测 Dispatcher 中仍存在数字 `planner_template_id`，输出带 Agent 名称的明确警告。
- 在 Agent 编辑页提供“恢复默认”支持，恢复后的正文使用 nkey。
- 自定义版本由用户确认后手动迁移；保留历史版本，可回滚。

### 4.4 `server/server_mgmt.go`

当前通用“恢复默认”只覆盖 `BuiltinPromptSeeds()` 中的内置 Agent。为 `vuln_dispatcher` 增加明确默认提示词入口，使其可以恢复为 nkey 版本。

恢复默认仍通过现有 `ResetPromptToDefault` 追加并切换版本，不删除历史。

### 4.5 配置包

更新 `config-export/agent_prompts_current.jsonl` 中 Dispatcher 正文，确保不包含环境相关数字 ID。

`start.sh export-config` 无需保存额外 ID 映射；当前提示词本身已经使用 nkey。

`start.sh import-config` 在导入 Dispatcher 提示词前检查：

- 新格式 nkey 可以导入。
- 旧格式包含数字 `planner_template_id` 时拒绝该提示词导入，并提示先转换为 nkey；不能猜测源环境 ID 的语义。
- 其余配置的导入顺序和单文件事务语义保持不变。

`config-export/README.md` 增加说明：Planner 模板通过 nkey 关联，数据库数字 ID 不属于可移植配置。

## 5. 当前环境的临时处置

在未实施代码方案前，只允许做本机临时修复：

| 场景 | 临时 ID |
|---|---:|
| 内网渗透 | 4 |
| 权限提升 | 5 |
| 漏洞利用 | 6 |
| 漏洞挖掘 | 2 |
| 代码审计 | 3 |

此表只适用于当前数据库，不能再导出到其他环境作为通用配置。

若暂时不能修改提示词，可保持 Dispatcher 触发器开启：当前错误会拒绝创建任务，不会错误派发；但功能不可用。应记录失败并尽快修复，而不是删除模板校验。

## 6. 兼容性

- 旧调用方仅传有效 `planner_template_id` 时行为不变。
- 新 Dispatcher 只传 `planner_template_nkey`。
- 同时传 ID 和 nkey 返回 `400` 或工具参数错误。
- nkey 缺失、空字符串或不存在时失败关闭，不创建任务、不使用默认模板。
- 模板正文仍在任务创建时快照，后续修改模板不影响已创建任务。
- 不修改 `planner_templates` 表结构，不迁移现有 BIGSERIAL ID。

## 7. 测试

### 7.1 后端测试

1. 五个 nkey 分别解析到当前环境的正确模板。
2. 两个环境具有不同模板 ID，同一 nkey 均创建正确场景任务。
3. 不存在的 nkey 返回错误，任务数为 0。
4. ID 与 nkey 同传返回错误，任务数为 0。
5. `planner_prompt` 与模板引用同传返回歧义错误。
6. 旧数字 ID 调用仍可创建任务。
7. nkey 解析后的模板正文快照与直接 ID 调用完全一致。
8. 内网渗透等分类的 `default_review_enabled` 仍按同一个模板正确生效。
9. 模板在解析后被并发修改或删除时不产生半创建任务。

### 7.2 配置与迁移测试

1. 从环境 A 导出、导入到模板 ID 不同的环境 B，Dispatcher 仍选择正确场景。
2. 重复导入同一配置幂等，不重复追加相同提示词版本。
3. 旧数字 ID 配置包得到明确迁移错误，不静默写入。
4. 用户自定义提示词不被启动升级覆盖。
5. “恢复默认”得到 nkey 版本，历史版本仍可查看。

### 7.3 端到端验收

在测试数据库和测试 LLM 下，让 Dispatcher 分别选择五类场景，断言：

- 实际创建任务的模板快照符合所选场景。
- 没有数字 ID 参数。
- 模板缺失时不创建任务。
- 子任务审核开关与既有规则一致。

不得用真实目标做本项测试。

## 8. 发布顺序

1. 备份数据库和当前提示词版本。
2. 先发布兼容 nkey 的 `spawn_task`，保留旧 ID 入口。
3. 更新默认提示词、恢复默认入口和配置包。
4. 运行后端、迁移和导入导出测试。
5. 在测试环境验证五类派发后，再迁移当前数据库提示词。
6. 观察日志中不再出现数字模板警告后完成发布。

验收标准：**同一配置包在模板 ID 完全不同的两个环境中，仍按场景名称选择同一个逻辑模板；模板缺失时绝不静默降级。**

## 9. 实施变更记录

实施时必须单独维护 `docs/ARTEX-漏洞派发模板引用修复-实施记录.md`，不能与清理审批或其他功能共用完成报告。至少记录：

- `spawn_task` schema、参数结构、校验和模板快照代码的修改前后签名。
- 默认提示词、`config-export` 和当前数据库提示词的版本变化。
- 新增测试、旧调用兼容测试、迁移结果及实际提交号。
- 对线上配置的操作时间、备份位置和恢复方法。

文档以实际 diff 和测试结果为准，不把上述计划文件清单当成已修改文件。
