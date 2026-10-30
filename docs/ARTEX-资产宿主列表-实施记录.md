# ARTEX 资产宿主列表实施记录

> 状态：代码、测试、真实浏览器验证和 Docker 部署均已完成；尚未提交 Git。
> 页面：`/function/assets/` 的“宿主”Tab，不是独立的发现页面。

## 1. 目标

将资产按 IP 宿主投影为一行，展示企业、主域名、子域名、IP、应用/服务/接口、发现数。点击整行后，在同一页面下方筛选该宿主的发现；保留原有资产类型视图。

## 2. 文件变更总表

| 类型 | 文件 | 变更 |
|---|---|---|
| 新增 | `db/host_assets.go` | 宿主 DTO、查询、聚合、排序和筛选 |
| 新增 | `db/host_assets_test.go` | 聚合、裸 IP、发现去重、精确 asset_ids 查询测试 |
| 修改 | `db/findings.go` | `FindingFilter` 新增 `AssetIDs []int64` |
| 修改 | `db/finding_assets.go` | 发现查询复用精确资产 ID 条件 |
| 修改 | `server/assets.go` | `GET /api/assets?type=host` 返回宿主投影 |
| 修改 | `server/findings_groups.go` | 解析 `asset_ids` 逗号参数 |
| 新增 | `web/src/app/(main)/function/assets/_components/host-assets.tsx` | 六列表格和同页发现列表 |
| 修改 | `web/src/app/(main)/function/assets/page.tsx` | 新增宿主 Tab、查询状态、分页和行选择 |
| 修改 | `web/src/lib/types.ts` | 新增宿主 DTO 和发现查询类型 |
| 修改 | `web/src/lib/api.ts` | 新增 `hostAssets`，序列化 `asset_ids` |
| 新增 | 本文 | 实施、验证与边界记录 |

## 3. 后端实现

### 3.1 宿主投影

`QueryHosts` 复用资产范围、企业、任务和 DSL 条件，不把 `BuildFindingAssetTree` 的单 parent 树直接平铺。

- 以规范化 IP 为宿主键；裸 IP 保留，域名字段为空。
- A/AAAA/CNAME 链解析到最终 IP；多个子域名聚合同一宿主，主显示一个并保留全部。
- IP 的绑定域名、应用、服务、接口归到对应宿主。
- 同一资产重复出现不会重复增加企业票数；企业票数和 ID 决定稳定企业显示。
- 同一 finding 关联同宿主多个资产时只计数一次，严重度按 finding 去重后统计。
- 每行保留 `asset_ids`，发现筛选使用这一稳定范围，不靠页面字符串二次猜测。
- 排序使用发现数和严重度，筛选后的显示计数与稳定范围分离。

### 3.2 API

```text
GET /api/assets?type=host&company_id=&task_id=&dsl=&limit=&offset=
GET /api/findings?asset_ids=1,2,3&...
```

原资产类型 API 和 `/function/findings` 页面未改成宿主页面。

## 4. 前端实现

- 在原有企业/根域名/IP/子域名/应用/服务/接口 Tabs 后增加“宿主”。
- 六列顺序与需求一致；多子域名显示 `+N 其他`，服务/接口保留方法和 URL 信息。
- 整行支持鼠标和键盘选择，再次点击取消。
- 选中后在表格下方加载发现列表，支持严重度筛选和任务信息展示。
- 表格容器使用横向滚动，760px 窄屏不压缩六列内容。

## 5. 测试与浏览器发现的问题

`db/host_assets_test.go` 覆盖多子域名、CNAME、服务/接口、发现去重、裸 IP、真实 PostgreSQL 查询和精确发现筛选。

真实 Chrome 初次验证发现裸 IP 的 `subdomains` 为 `null`，页面读取数组时崩溃：

1. 修改 `db/host_assets.go`，使用非 nil 空切片，API 统一返回 `[]`。
2. 修改 `db/host_assets_test.go`，增加裸 IP 的非 nil 断言。
3. 聚焦测试、全量测试和浏览器复验通过。

## 6. 最终验证

- `go test -p=1 ./...`、`go vet ./...`：通过。
- `npx tsc --noEmit`、既有 Node 测试、`npm run build:static`：通过。
- 真实 Chrome：宿主 Tab、六列、服务/接口聚合、行筛选、取消筛选、同页发现、760px 横向滚动通过。
- 截图：`/root/artex-assets-ui.png`、`/root/artex-assets-ui-narrow.png`。
- 临时任务、资产、发现、探索和企业已全部删除。

## 7. 代码级补充：DTO 与查询

`db/host_assets.go` 新增的实际返回结构：

```go
type AssetHostRow struct {
    Key          string             `json:"key"`
    CompanyID    int64              `json:"company_id,omitempty"`
    CompanyName  string             `json:"company_name,omitempty"`
    RootDomain   string             `json:"root_domain,omitempty"`
    Subdomains   []string           `json:"subdomains"`
    IP           string             `json:"ip"`
    Services     []AssetHostService `json:"services"`
    AssetIDs     []int64            `json:"asset_ids"`
    FindingCount int                `json:"finding_count"`
    Critical     int                `json:"critical"`
    High         int                `json:"high"`
    Medium       int                `json:"medium"`
    Low          int                `json:"low"`
}

type AssetHostService struct {
    AssetID int64  `json:"asset_id"`
    Type    string `json:"type"`
    Label   string `json:"label"`
    URL     string `json:"url,omitempty"`
    Method  string `json:"method,omitempty"`
}
```

`QueryHosts` 当前不是一条 SQL 聚合查询，而是：

```go
rows, err := s.db.Query(assetSelectCols + " ORDER BY id")
// scanAssets + companies + findings
hosts := buildAssetHostRows(assets, companyNames, hits)
// 在宿主行上应用 company/task/DSL 过滤，最后 offset/limit。
```

代码中保留了明确的简化标记：

```go
// shortcut: assemble in memory like the finding asset tree; move this projection
// to SQL when full asset scans become measurable in production.
```

因此当前实现功能完整，但资产量很大时需要把投影和分页下推 SQL。

## 8. 代码级补充：非 nil 数组修复

问题来自 Go JSON 序列化：nil slice 输出 `null`，前端按数组读取会报错。

现在所有宿主都通过统一构造函数初始化：

```go
func newAssetHostBuilder(ip string) *assetHostBuilder {
    return &assetHostBuilder{
        row: AssetHostRow{
            Key: "host:" + ip, IP: ip,
            Subdomains: []string{}, Services: []AssetHostService{}, AssetIDs: []int64{},
        },
        assetSet: map[int64]bool{}, rootSet: map[string]bool{},
        subdomainSet: map[string]bool{}, companyVotes: map[int64]int{},
    }
}
```

裸 IP 现在返回：

```json
{"root_domain":"","subdomains":[],"services":[],"asset_ids":[1]}
```

而不是 `subdomains:null`。

## 9. 代码级补充：发现筛选

`db/findings.go`：

```go
type FindingFilter struct {
    // 既有字段省略
    AssetIDs []int64
}
```

`server/findings_groups.go:31` 统一解析，列表/分组/导出共用：

```go
for _, raw := range strings.Split(q.Get("asset_ids"), ",") {
    if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64);
        err == nil && id > 0 && len(assetIDs) < 1000 {
        assetIDs = append(assetIDs, id)
    }
}
```

`db/finding_assets.go` 把 ID 转为 JSONB 包含条件，复用 `idx_findings_asset_ids`：

```go
func assetIDContainments(ids []int64) []string {
    out := make([]string, 0, len(ids))
    for _, id := range ids {
        out = append(out, "["+strconv.FormatInt(id, 10)+"]")
    }
    return out
}
```

前端请求示例：

```http
GET /api/findings?asset_ids=12,18,25&severity=high&limit=20&offset=0
```

## 10. 前端关键状态

`web/src/app/(main)/function/assets/page.tsx` 增加：

```tsx
const [hostRows, setHostRows] = React.useState<AssetHostRow[]>([]);
const [selectedHostKey, setSelectedHostKey] = React.useState<string | null>(null);
```

`host-assets.tsx` 行点击只做两件事：

1. 切换 `selectedHostKey`。
2. 把该行 `asset_ids` 传给发现查询，在同页下方展示结果。

没有新增 `/function/findings` 路由，也没有把发现页替换成宿主页面。

## 11. 回滚

1. 从资产页移除 `host` Tab 和 `HostAssets` 组件。
2. 删除 `hostAssets` 前端 API 和 `asset_ids` 查询参数。
3. 移除后端 `type=host` 分支和 `FindingFilter.AssetIDs`。
4. 删除 `db/host_assets.go` 及测试。

本功能没有新表或数据库列，不需要数据回滚；现有资产和发现不会因代码回滚被删除。

## 12. 测试与实现代码逐行对照

| 测试/验证 | 测试位置 | 对应实现位置 | 覆盖点 |
|---|---|---|---|
| `TestBuildAssetHostRowsAggregatesHostAndDeduplicatesFindings` | `db/host_assets_test.go:9` | `db/host_assets.go:166` `newAssetHostBuilder`、`:186` `buildAssetHostRows` | 多子域名/CNAME 聚合、服务接口、同宿主发现去重、裸 IP 空数组。 |
| `TestQueryHostsAndExactAssetFindingFilter` | `db/host_assets_test.go:47` | `db/host_assets.go:57` `QueryHosts`；`db/findings.go:164` `FindingFilter.AssetIDs` | 真实 PostgreSQL 投影、分页和精确 asset_ids 筛选。 |
| HTTP 资产查询 | 浏览器 `/function/assets/` | `server/assets.go:319` `listAssets` | `type=host` 返回宿主行，不改变既有七类资产接口。 |
| HTTP 发现筛选 | 点击宿主行 | `server/findings_groups.go:33-47`；`db/finding_assets.go:669` | 逗号 asset_ids 解析和 JSONB 精确筛选。 |
| 六列与表下发现 | 真实 Chromium 桌面/760px | `web/src/app/(main)/function/assets/_components/host-assets.tsx:20` `HostAssets` | 六列、行筛选、取消筛选、同页发现、窄屏横向滚动。 |

浏览器证据：`/root/artex-assets-ui.png`、`/root/artex-assets-ui-narrow.png`。浏览器发现的 `subdomains=null` 修复对应 `db/host_assets.go:168` 的非 nil 空切片。

## 13. 默认视图修正（2026-10-29 复验）

### 问题与原代码

用户直接打开 `/function/assets/` 时仍看到旧的“企业”表格。此前只增加末尾“宿主”Tab，没有改变默认 Tab，因此不符合“资产页面直接显示宿主格式”的要求。

```tsx
// 原 web/src/app/(main)/function/assets/page.tsx
{ key: "company", label: "企业", icon: Building2Icon },
// ...
{ key: "host", label: "宿主", icon: NetworkIcon },
const [tab, setTab] = React.useState("company");
```

### 修改后的代码

`page.tsx:103` 把宿主放到第一个 Tab；`:118` 修改默认状态：

```tsx
const TABS = [
  { key: "host", label: "宿主", icon: NetworkIcon },
  { key: "company", label: "企业", icon: Building2Icon },
  // 其余既有分类不变
];
const [tab, setTab] = React.useState("host");
```

宿主内容仍由原来的组件渲染，没有复制第二套表格：

```tsx
// page.tsx:715
<HostAssets rows={hostRows} selectedKey={selectedHostKey} onSelect={setSelectedHostKey} />
```

其他资产分类全部保留。本次没有修改资产 API 或数据库投影，因为问题只在默认 Tab。

### 测试与实现对应关系

| 验证 | 结果 | 对应实现 |
|---|---|---|
| Windows 浏览器直接打开 `/function/assets/` | 活动 Tab 为“宿主”，无需手点 | `web/src/app/(main)/function/assets/page.tsx:103,118` |
| 浏览器读取表头 | 企业、主域名、子域名、IP、应用/服务/接口、发现数六列 | `web/src/app/(main)/function/assets/_components/host-assets.tsx` |
| `npx tsc --noEmit`、`npm run build:static` | 通过 | React 状态、组件属性和实际静态产物 |

浏览器证据：

```text
C:\Users\Administrator\Downloads\artex-docs\artex-final-fix-browser-result.json
C:\Users\Administrator\Downloads\artex-docs\artex-assets-default-host.png
```

结果包含 `assets-default-host` 和 `assets-six-columns`。
