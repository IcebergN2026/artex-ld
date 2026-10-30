# ARTEX Review 审核内容行布局实施记录

## 状态

已实施并部署。只调整审核内容排版，不改变分栏比例、审核状态机、审批接口或聊天功能。

## 代码

文件：`web/src/app/(main)/function/tasks/detail/_tabs/review-panel.tsx`

`queueOnly` 内容区改为语义化 `<dl>` 行布局：

- 意图 ID
- 执行器
- 优先级
- 替代意图（存在时）
- 预审意见

标签列固定为 `4rem`，内容列使用 `minmax(0,1fr)`；长文本使用 `whitespace-pre-wrap break-words`，保留原版白灰风格，无新增图表或复杂卡片。

## 验证

- `npx tsc --noEmit`：通过。
- `npm run build:static`：通过。
- 真实 Chrome：桌面显示意图 ID/执行器/优先级/替代意图/预审意见，队列切换正常，右侧聊天输入正常。
- 390px：无横向溢出。
- 页面 JS 异常：0。
- 结果：`artex-review-rows-production-result.json`，`pass=true`。
- 截图：`artex-review-rows-production.png`、`artex-review-rows-production-mobile.png`。

## 部署说明

首次 `docker compose up -d --build artex` 因 Docker Hub DNS 超时失败；旧容器仍健康，不能据此认定新代码已部署。通过镜像内字符串检查确认旧二进制后，使用宿主机 `go build -tags embedui` 和现有运行镜像离线更新，再次确认新二进制包含“意图 ID”且旧“审核信息”已移除。

备份：`/root/artex-pre-review-rows-20261029-183753.dump`。

构建日志：`/root/artex-review-rows-web-build.log`、`/root/artex-review-rows-image.log`。

最终服务 `healthy`，临时 UI 任务和 exploration 已删除，临时 HTTP、令牌与构建目录已清理。
