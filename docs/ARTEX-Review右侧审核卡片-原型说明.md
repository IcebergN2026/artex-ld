# ARTEX Review 右侧审核卡片原型说明

## 最终交付

`C:\Users\Administrator\Downloads\artex-docs\artex-review-original-right-card.html`

证据：

- `artex-review-original-right-card-queue.png`
- `artex-review-original-right-card-queue-mobile.png`
- `artex-review-original-right-card-result.json`

## 原版基础

最终版直接捕获原版 ARTEX `/function/tasks/detail?id=21` 的真实 DOM 与 7 个原始样式表，移除应用脚本后注入右侧审核卡片。示例 IP 和凭据已替换，不修改生产页面或业务代码。

早期 `artex-review-right-card.html` 仅为近似风格，已由原版 DOM 原型取代。

## 多审核队列与紧凑尺寸

- 单个非模态右侧卡片，宽度由 410px 缩小到 360px，不叠放多个框。
- 示例包含 3 项审核，显示待审数量、当前位置及数字快捷切换。
- 支持上一项、下一项、命令/证据展开。
- 允许或拒绝后自动进入下一待审项；已处理项可回看。
- 拒绝必须填写原因；全部处理后显示 0 项待审。
- 支持关闭、重新打开和重置示例；390px 移动端不横向溢出。

## Chrome 验证

真实 Chrome CDP 验证 `pass=true`：原始样式保留、宽 360px、3 项队列、切换、详情、自动下一项、允许、拒绝、全部完成、已处理回看、重置及移动端均通过。

仅交付 HTML 原型，未修改线上 Review 页面。
