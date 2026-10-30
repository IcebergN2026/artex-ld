# ARTEX 内网子任务宽泛描述与信息收集实施记录

状态：已实施、已部署  
实施日期：2026-10-29

## 1. 需求与调查结论

用户要求：

1. 内网子任务描述进一步放宽，不再出现“网络可达不等于已授权”“范围外主机只做被动记录”等文案。
2. 排查新建内网子任务未做信息收集就完成的问题。

正式库实际记录：

- 外网父任务 `#20` 已完成。
- 内网子任务 `#21` 仍为 `created`，不是 `done`。
- 四个 Goal 均为 `open`。
- 意图 `#237` 负责两个容器的环境枚举与凭据提取，状态为 `running`。
- 意图 `#238` 负责 2003 端口与 SSH 信息收集，状态为 `running`。
- 两个意图在人工审核通过后由 Worker 领取。

因此本次没有复现“子任务已被系统提前标记完成”。用户看到的是父任务已完成，而子任务仍在执行。本次不增加无依据的引擎修复。

## 2. Dispatcher 描述调整

文件：`agent/promptcatalog.go`  
正式库：Dispatcher 当前提示词版本 11。

### 2.1 目标范围

新规则：

```text
目标范围：围绕父任务证据中的已知资产、当前立足点、容器/网卡/路由信息及可继续探索的内网主机和服务展开；不限于单一立足点，不继承父任务单次单资产限制，不默认生成单资产限制，不额外生成“范围外只记录”等约束。
```

删除了：

```text
可达性不等于授权
范围外主机只做被动记录
未获授权前不得对其发起攻击
不得把可达但未授权的主机写成可攻击目标
```

### 2.2 完成目标

```text
先完成主机、容器、网络、服务、账户与凭据等信息收集，再说明需要交付的权限提升、横向路径和实测证据。
```

### 2.3 内网 Goal

```text
从已验证立足点出发，先全面收集主机、容器、网卡、路由、内网网段、存活主机、服务、账户/凭据与域环境，再获取内网最高权限；尽可能拓展可控内网主机，定位管理员、域控或其他关键管理主机并横向移动，交付可验证证据。不得仅凭父任务已有结论或当前任务尚未执行就判定完成。
```

`name`、七段 `description`、Finding 追加和双模式规则保持不变。

## 3. 内网 Planner 强制信息收集

文件：`agent/planner_domain_templates.go`  
内置模板：`plannerDomainIntranetTmpl`  
正式库：`planner_templates.nkey=内网渗透`，实际记录 `id=4`。

### 3.1 开局完成门槛

```text
当前任务尚无 Worker 产出的信息收集 fact，且没有 open/running 的信息收集意图时，禁止把全部目标标 met 或直接结束；父任务及继承证据只能作为线索，必须先为当前任务派发信息收集意图。
```

### 3.2 开局强制前置

```text
先派当前任务的信息收集意图，覆盖立足点主机/容器上下文、网卡/路由/内网网段、存活主机与服务、账户/凭据及必要的域环境；优先复用已有事实与可用通道，不指定固定命令。当前任务尚无此类 Worker fact 或 open/running 意图时，不得直接结束。
```

这里使用提示词而不是 Skill：Planner 负责规划与验收，必须先派信息收集意图；具体收集步骤和工具用法才属于 Worker Skill。

## 4. 自动化测试

### 4.1 Planner

`agent/internal_planner_prompt_test.go::TestInternalPlannerLowNoiseAndOptionalMCP`

新增断言：

- `开局完成门槛`
- `必须先为当前任务派发信息收集意图`
- `开局强制前置`
- `不指定固定命令`

同时保留低噪声、可选 MCP 和不设置固定域控/网段完成条件的断言。

### 4.2 Dispatcher

`server/vuln_dispatcher_nkey_test.go::TestVulnDispatcherDefaultPromptUsesStableTemplateKeys`

验证：

- 新 Goal 包含全面信息收集。
- 不允许尚未执行就判定完成。
- 不再包含用户指出的三种限制文案。

双模式、Finding 追加、短名称、证据继承和配置导出一致性测试继续通过。

### 4.3 全量验证

```bash
ARTEX_PG_DSN=... go test -p=1 ./...
go vet ./...
git diff --check
```

全部通过。

日志：

- `/root/artex-internal-info-go-test.log`
- `/root/artex-internal-info-go-vet.log`

## 5. 正式库与配置导出

备份：

```text
/root/artex-pre-internal-info-20261029-144813.dump
```

更新：

- 新增 Dispatcher 提示词版本 11，并切换 `agents.current_prompt_id`。
- 按 `nkey=内网渗透 AND builtin=true` 更新 Planner 模板。
- 重新导出 `config-export` 和 `artifacts/config-export`。

实施中第一次使用 `id=17` 更新 Planner 没有命中，因为 `17` 是 Worker 内网模板；随后按稳定 `nkey` 更新正确的 Planner 内置模板。该次 `UPDATE 0` 没有产生错误数据。

## 6. 部署与边界

```bash
docker compose up -d --build artex
```

日志：`/root/artex-internal-info-deploy.log`  
结果：`/api/health` 返回 `ok=true`，容器为 `healthy`。

- 当前正在运行的 #21 保留原任务快照，不被中途改写。
- 新规则影响后续使用更新后 Planner 模板和 Dispatcher 提示词的任务。
- 未执行真实 LLM 或目标网络测试。
- 未修改 Goal/Worker 代码和任务完成状态机。
