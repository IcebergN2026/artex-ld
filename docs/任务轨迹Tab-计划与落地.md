---

# 第四部分:结果块折叠(2026-11-16)

用户反馈:轨迹时间线里规划者的规划结果(如 #919 的 result 步骤)整块 markdown 常开,把后续时间线顶出屏幕;思考/工具/文本块均已折叠,唯独 result 不折叠。

## 变更

| 文件 | 变更 |
|---|---|
| `web/src/components/transcript.tsx` | `AnswerBlock` 新增 `collapsible` prop:true 时默认折叠为一行(chevron + 旗标 + worker chip + `#intent` 标签 + 截断摘要 + 右侧「总结」提示),点击展开全文(markdown/错误文本)+ 时间;折叠态不发 detail 请求,展开时经 `useInView` + `open` 双条件懒加载。`ExecView` 按 `collapsible={!chat}` 传入——对话页(main agent 回答)保持全文常开不变,轨迹页与会话页非 main lane 的所有 result 块默认折叠。 |

零后端改动;`groupSteps` 的 answer 分派不变,minimap 色块/抽屉定位(`data-seq` 锚点)不受折叠影响。

## 验证

- `npx tsc --noEmit`、`npm run build` 通过;biome 对改动行无新增诊断。
- `NEXT_PUBLIC_MOCK=1` dev server 实测(t-acme-web 轨迹页):11 个 result 行默认折叠;点开一行 → markdown 全文 + 时间渲染、右侧抽屉同步打开;再点 → 收起,恢复 11 行折叠态。
- 对话页(主 Agent 会话)回归:5 个 answer 块全部全文常开,无折叠按钮。

---

# 第四部分:结果块折叠(2026-11-16)

用户反馈:轨迹时间线里规划者的规划结果(如 #919 的 result 步骤)整块 markdown 常开,把后续时间线顶出屏幕;思考/工具/文本块均已折叠,唯独 result 不折叠。

## 变更

| 文件 | 变更 |
|---|---|
| `web/src/components/transcript.tsx` | `AnswerBlock` 新增 `collapsible` prop:true 时默认折叠为一行(chevron + 旗标 + worker chip + `#intent` 标签 + 截断摘要 + 右侧「总结」提示),点击展开全文(markdown/错误文本)+ 时间;折叠态不发 detail 请求,展开时经 `useInView` + `open` 双条件懒加载。`ExecView` 按 `collapsible={!chat}` 传入——对话页(main agent 回答)保持全文常开不变,轨迹页与会话页非 main lane 的所有 result 块默认折叠。 |

零后端改动;`groupSteps` 的 answer 分派不变,minimap 色块/抽屉定位(`data-seq` 锚点)不受折叠影响。

## 验证

- `npx tsc --noEmit`、`npm run build` 通过;biome 对改动行无新增诊断。
- `NEXT_PUBLIC_MOCK=1` dev server 实测(t-acme-web 轨迹页):11 个 result 行默认折叠;点开一行 → markdown 全文 + 时间渲染、右侧抽屉同步打开;再点 → 收起,恢复 11 行折叠态。
- 对话页(主 Agent 会话)回归:5 个 answer 块全部全文常开,无折叠按钮。

## 部署记录(<DEPLOY_HOST_B>:8787)

- 该机此前为基线 `0.1.6`(无轨迹功能);本次随折叠改动一并同步当前工作区(轨迹 Tab + minimap/抽屉 + 本折叠变更),无迁移、零 db 改动。
- 流程:`NEXT_EXPORT=1 npx next build`(npm script 内联 env 在 Windows cmd 不生效,需 bash 设变量)→ `web/out` 覆盖 `server/webui/dist` → `GOOS=linux GOARCH=amd64 go build -tags embedui -ldflags "-X main.version=0.1.7-traj-fold" ./cmd/artex` → scp。
- sha256 双端一致(`0bb3ba0f…f20e8e`);按 `pgrep -x artex` 精确停旧进程(PID 1409294),备份 `artex.bak-trajfold-20261116-013105`,`nohup ./start.sh` 重启。
- 验证:`/api/health` → `0.1.7-traj-fold`;`/login` 200;二进制内含「展开结果全文」新 UI 代码;`session=all` history/stream 路由未鉴权 401(在线)。
- 回滚:恢复 `artex.bak-trajfold-20261116-013105` 为 `artex` 后重启 start.sh。
