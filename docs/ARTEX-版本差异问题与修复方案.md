# ARTEX 版本差异问题分析与修复方案

日期：2026-11-04  
对比基线：`D:/projects/20261103/artex-ld`  
文档性质：问题分析、修复方案与实施记录  
实施日期：2026-11-05

## 1. 范围与结论

本文记录 Docker 版本注入、拓扑和预审提示词问题。各项按“问题情况 → 后果 → 修复方法 → 验收标准”展开。

已经完成的 Tools / Agents 内置及 `config-export` 同步不属于本文的待修问题。本文第 2～4 节所述三项修复已实施，第 6 节记录实际修改和远程 Linux 验证结果。

Finding 派发维持现有实现，不列入修改范围：单 Planner 正常完成校验、持久化和入队，数据库负责调用幂等，队列按 serial/by_task 合并，10 分钟兜底仅作为异常场景的最后保障。

### 1.1 核心结论

**Docker 构建必须恢复正式版本注入。** 构建时注入本次产物的版本号，运行时再与最新 Release 比较；不应在构建时联网获取或写死所谓的“最新版本”。

### 1.2 建议优先级

| 优先级 | 事项                       | 主要原因                        |
| --- | ------------------------ | --------------------------- |
| P1  | Docker 正式版本注入            | 当前 Docker 产物无法正常使用版本检查和一键更新 |
| P2  | 拓扑 busy、异常与 localStorage | 避免重复写入、无提示失败和用户布局被覆盖        |
| P3  | 预审提示词契约                  | 统一提示词、解析校验和审核展示口径           |

## 2. Docker 构建版本号恒为 dev

### 2.1 问题情况

基线的版本注入链为：

```text
.env / docker-compose
  → ARTEX_VERSION
  → Dockerfile.source ARG
  → -X main.version=<版本>
```

当前 `Dockerfile.source` 已移除 `ARG ARTEX_VERSION` 和对应 `-ldflags`，因此 `cmd/artex/main.go` 中的 `var version = "dev"` 不再被覆盖。

### 2.2 后果

- 启动 banner 显示 `dev`。
- `/api/health` 返回 `"version":"dev"`。
- `selfupdate.CompareVersions("dev", latest)` 无法比较。
- 更新接口 `comparable=false`、`has_update=false`，一键更新实际不可用。

相关代码：`Dockerfile.source:13-19`、`server/update.go:249-250`。

### 2.3 修复方法

恢复完整注入链。

`Dockerfile.source` 的 Go 构建阶段：

```dockerfile
FROM golang:1.26-bookworm AS go-builder

ARG ARTEX_VERSION=0.1.4
WORKDIR /src

# COPY 依赖与源码保持不变

RUN CGO_ENABLED=0 go build \
      -tags embedui \
      -trimpath \
      -ldflags "-s -w -buildid= -X main.version=${ARTEX_VERSION}" \
      -o /out/artex ./cmd/artex
```

`docker-compose.yml`：

```yaml
build:
  context: .
  dockerfile: Dockerfile.source
  args:
    ARTEX_VERSION: ${ARTEX_VERSION:-0.1.4}
```

`.env.example`：

```dotenv
ARTEX_VERSION=0.1.4
```

当前构建与发布版本统一为 **0.1.4**，对应 Git Tag **v0.1.4**。`.env.example`、Compose、Dockerfile、README 和 CHANGELOG 必须保持一致；不得在构建时联网获取最新版本。

不要在 Docker 构建时联网获取最新版本。构建版本必须固定、可复现；运行时才与最新 Release 比较。

### 2.4 验收标准

- `/api/health` 返回实际构建版本，不再是 `dev`。
- 更新接口返回 `comparable=true`。
- 当前版本等于最新 Release 时，`has_update=false` 属于正常结果。
- 出现更高版本 Release 时，`has_update=true`。

## 3. 拓扑归组功能问题

### 3.1 新按钮缺少统一错误处理与 busy 保护

`topology-toolbar.tsx:318-321` 在 `layoutGraph` 的 `try/finally` 之外调用 `applyC24Grouping`。

后果：

- API 失败时出现 unhandled rejection，用户看不到明确错误。
- 调用期间按钮没有立即禁用，快速双击可能重复发送建组和节点移动请求。
- 多个写请求不是事务，部分成功后失败时，界面和服务端可能不一致。

修复：

- 抽取统一的 `runWithBusy`，包裹整个“归组 + 布局”过程。
- 进入时立即 `setBusy(true)`，`catch` 显示 toast，`finally` 恢复按钮。
- 失败后重新加载拓扑，并明确提示“部分修改可能已生效”。

### 3.2 localStorage 读取在异常保护之外

`topology-editor.tsx:69` 的 `getItem` 位于 `try` 外。禁用存储、沙箱 iframe 或部分隐私环境下可能抛出 `SecurityError`，导致自动布局中断且没有错误提示。

修复：将读取和写入都放进 `try/catch`；存储不可用时按“尚未归组”处理，不阻断拓扑显示。

### 3.3 手动归组不写一次性标记

用户手动归组后再解散，刷新页面可能又自动归组，覆盖手动调整结果。

修复：

- 自动和手动归组成功后，都写同一个 `topology-c24-grouped-${taskId}` 标记。
- 封装统一的 `markC24Grouped(taskId)`，避免两个组件分别拼接 key。
- 如果提供“重置拓扑并允许重新自动归组”，重置时同步清除标记。

### 3.4 验收标准

- 归组 API 失败时有明确提示，且没有未处理 Promise rejection。
- 快速双击只产生一轮写操作。
- localStorage 不可用时页面仍可使用。
- 手动解散后刷新不会被意外重新归组。

## 4. 预审提示词契约冲突

### 4.1 问题情况

`agent/promptcatalog.go:206` 写“`note: ≤200字，revise 时必填`”，紧接着 `:208` 又要求“pass 必须写一句通过理由”。

### 4.2 后果

模型可能在 `pass` 时留下空 `note`。当前代码不会强制校验，因此“pass 必须写理由”可能只是软约束，审核质量不稳定。

### 4.3 修复方法

如果产品要求 pass 也必须给理由，统一改为：

```text
note: ≤200字，pass / revise 均必填
```

并在服务端增加：

- `pass`、`revise` 的 `note` 去除空白后都必须非空。
- 保持 200 字截断。
- 增加 `pass + 空 note`、`revise + 空 note` 测试。

如果产品允许 pass 不写理由，则删除后面的强制要求。两种口径只能选择一种。

### 4.4 验收标准

- 模板说明、解析校验和 UI 展示口径一致。
- 两种 verdict 的空 note 都按同一契约处理。
- 合法 note 的截断行为不发生回归。

## 5. 综合验收清单

| 范围         | 必须通过的检查                                                     |
| ---------- | ----------------------------------------------------------- |
| Docker 版本  | health 显示正式版本、版本比较单测通过；存在正式 Release 时 comparable=true |
| 拓扑         | 无 unhandled rejection、无重复提交、手动布局不被意外覆盖                      |
| 预审         | 提示词、解析校验、UI 和测试口径一致                                         |
| 回归         | `go vet ./...`、`go test -p=1 ./...`、前端检查及真实 PostgreSQL 启动验证 |

## 6. 实施与验证记录（2026-11-05）

### 6.1 已实施修改

- **Docker 版本与启动链**：当前版本统一为 **0.1.4**。`Dockerfile.source` 恢复 `ARTEX_VERSION` 和 `-X main.version=${ARTEX_VERSION}` 注入，Compose 与 `.env.example` 同步；增加标准 `GOPROXY` 构建参数，默认值保持官方代理，可在网络受限环境覆盖。`Dockerfile` 与 `Dockerfile.source` 同时补齐 `start.sh` 启动必需的 `scripts/artex-config.sh` 和升级 SQL，修复容器入口启动即退出。
- **拓扑归组**：新增同步 `busyRef` 防重入，busy 覆盖归组和布局全过程；错误提示包含部分修改风险并尝试重载整图。统一 `isC24Grouped` / `markC24Grouped`，localStorage 读写异常不阻断页面；自动和手动归组共享同一标记。
- **预审契约**：人工预审 `pass` / `revise` 的 `note` 均必填，空值不进入结果，合法值执行 trim。同步代码默认提示词、升级 SQL、配置导出及确定性 provider 测试；自动处置契约和 200 字服务端截断未改变。
- **版本元数据**：README、CHANGELOG、`web/package.json`、`web/package-lock.json` 与 Docker 构建参数统一为 0.1.4。

### 6.2 远程 Linux 验证

验证使用独立的 Go 1.26、Node 22、PostgreSQL 16 容器和全新数据库，未使用 Windows 测试结果替代 Linux 结果。

| 检查 | 实际结果 |
| --- | --- |
| `go test -p=1 ./...` | 全部通过，包含 Tools / Agents 内置、`trace_cleaner`、`evidence`、`server` 等全部 Go 包 |
| `go vet ./...` | 通过 |
| `bash scripts/test-deploy-scripts.sh` | 全部通过；远程副本恢复 tar 丢失的可执行位后执行 |
| 前端 `npm run build:static` | 编译、TypeScript 和 31 个静态页面生成全部通过 |
| 拓扑修改文件 Biome | 无错误，仅保留一条既有 CSS 排序 info |
| 前端完整 `npm run check` | 仓库既有基线仍有 165 errors / 124 warnings；未将这些无关问题计为本次回归 |
| Docker 正式镜像构建 | 构建成功，日志确认 `-X main.version=0.1.4` |
| Docker 实例启动 | `/api/health` 返回 `{"ok":true,"service":"artex","version":"0.1.4"}`，首页 HTTP 200 |
| 全新数据库内置结果 | 11 个 Agent、59 个工具、3 个触发器，运行时提示词与代码默认一致 |
| 更新检查 | 当前版本为 0.1.4；配置的 GitHub 仓库返回“尚未发布任何正式版本”，故 live 接口未产生 `comparable`，版本比较单元测试已通过 |

### 6.3 浏览器实际验收

在远程 Docker 实例中创建内网任务及同 /24 的真实拓扑节点，完成以下交互：

1. 首次加载成功自动归组并展示 0.1.4 前端。
2. 删除分组后快速双击“按 C 段归组”：`POST /topology/groups` **仅 1 次**，最终只有 1 个分组，无未处理 JavaScript rejection。
3. 禁用拓扑 localStorage 键的读写：页面和归组仍正常，未出现页面异常。
4. 制造归组 API 失败：页面显示“部分修改可能已生效”并尝试重新加载；重载失败也有提示，无未处理 JavaScript 异常。
5. 手动归组后删除分组再刷新：已有归组标记阻止意外自动归回。
6. Agent 管理中分别打开“漏洞派发”和“痕迹清理”的触发页：均显示 `工具调用(dispatch_finding_to_agent)`，不再显示“无条件”；`on_tool_call` 保持关闭，现有显式派发行为不变。

首轮验证结束后已清理隔离浏览器、源码副本、测试数据库、网络和镜像，未修改远程现有部署或业务数据库。根据用户验收要求，后续重新启动了临时验收实例，并接入宿主机现有 Godzilla MCP；验收完成后应停止并清理该临时实例。
