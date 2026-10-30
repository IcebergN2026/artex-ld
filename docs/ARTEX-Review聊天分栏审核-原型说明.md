# Review 聊天分栏审核原型

## 最终交互

- 以真实 ARTEX 任务详情的 DOM、CSS、会话列表、聊天消息和输入框为底。
- 主聊天区域改为左右两栏：左侧审核约 30%，右侧原聊天约 70%；不是悬浮卡片。
- 桌面审核栏约 244.8px、聊天栏约 571.2px；窄屏改为聊天在上、审核在下。
- 多项审核在右栏通过数字、上一项/下一项切换；处理后自动进入下一项，可回看和重置。

## 文件

- HTML：`artex-review-split-chat.html`
- 桌面截图：`artex-review-split-chat-left.png`
- 移动端截图：`artex-review-split-chat-left-mobile.png`
- 验证结果：`artex-review-split-chat-left-result.json`

均位于：`C:\Users\Administrator\Downloads\artex-docs\`。

旧 `artex-review-original-right-card.html` 为已被取代的右侧卡片方案。

## Chrome CDP 验证

- 审核栏在左、聊天栏在右，宽度比约 30%:70%，审核栏 position=static。
- 原聊天输入框保留，原版 CSS 保留。
- 3 项审核、切换、允许后自动下一项、重置均通过。
- 390px 下聊天在上、审核在下，无横向溢出。
- `pass=true`。

仅修改独立 HTML 原型，未修改生产 Review UI。
