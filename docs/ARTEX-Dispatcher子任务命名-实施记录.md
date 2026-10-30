# Dispatcher 子任务命名实施记录

状态：已实施并部署。本文只记录子任务短名称功能，不改变 Finding 追加、七段 description 或 goal 的其他规则。

## 1. 需求与问题

用户要求 Dispatcher 新建子任务时，任务名使用：

```text
内网渗透 · <立足点 host>
```

此前 `spawn_task` 没有独立 `name` 参数，而 `description` 已按最新要求保存七段完整执行说明。如果继续把 description 当任务名，任务列表会显示大段证据和约束。

## 2. 字段职责

| 字段 | 当前职责 |
| --- | --- |
| `name` | 任务列表中的短名称，格式为“场景 · 主要目标”。 |
| `description` | 完整七段执行说明，包括模式、触发依据、目标起点、授权、完成目标、通道和纪律。 |
| `goal` | 内网最高权限、关键管理主机横向或仅漏洞交付目标。 |

## 3. 代码改动

### 3.1 spawn_task 工具

文件：`server/orchestration.go`

工具 schema 新增：

```go
"name": strParam("可选：任务列表显示的简短名称；内网子任务推荐「内网渗透 · <立足点 host>」"),
```

参数结构新增：

```go
Name string `json:"name"`
```

创建任务时写入已有 `db.TaskCreateOptions.Name`：

```go
t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
    Name:        strings.TrimSpace(a.Name),
    CategoryID:  categoryID,
    // 其余既有字段保持不变。
})
```

数据库已有 `tasks.name` 和任务列表显示逻辑，因此不需要新增列或修改前端。

### 3.2 Dispatcher 提示词

文件：`agent/promptcatalog.go`

新增规则：

```text
- name：必须填写简短任务名，格式为「<场景> · <主要目标>」。内网渗透写「内网渗透 · <立足点 host>」；权限提升写「权限提升 · <host>」；漏洞利用写「漏洞利用 · <资产/漏洞>」。任务名不要堆证据、步骤或约束。
- description：写完整的七段执行说明，不再兼任任务名。内容必须分条写清：
```

示例：

```text
name = 内网渗透 · 192.168.9.101
description = 七段完整执行说明
goal = 在授权范围内获取内网最高权限，定位关键管理主机并横向移动，交付可验证证据。
```

## 4. 配置与正式数据库

- `config-export/agent_prompts_current.jsonl` 已同步完整新提示词。
- 正式 `vuln_dispatcher` 已切换到提示词版本 9。
- 没有覆盖历史提示词版本。
- 没有修改历史任务的 name、description 或 goal。
- 旧调用方未传 name 时仍保持原兼容行为；新 Dispatcher 提示词要求必须传 name。

部署前备份：

```text
/root/artex-pre-task-name-20261029-134618.dump
```

备份权限为 0600。

## 5. 测试映射

| 功能点 | 验证 | 结果 |
| --- | --- | --- |
| 短名称规则 | `TestVulnDispatcherDefaultPromptUsesStableTemplateKeys` 检查 `name` 和“内网渗透 · <立足点 host>” | 通过 |
| Go 编译与既有派发 | 全量 `go test -p=1 ./...` | 通过 |
| 静态检查 | `go vet ./...` | 通过 |
| 正式提示词 | 数据库查询版本 9 且包含名称规则 | 通过 |
| 服务 | Docker health 与 `/api/health` | 通过 |

测试日志：

```text
/root/artex-task-name-go-test.log
/root/artex-task-name-go-vet.log
/root/artex-task-name-deploy.log
```

## 6. 验证边界

本轮验证的是工具参数、提示词契约和现有任务创建链，没有执行真实 LLM 自主派发，也没有连接真实目标。旧子任务不会被批量改名。
