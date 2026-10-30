# Review 聊天分栏审核实施记录

## 1. 需求与定稿

按用户确认的 HTML 原型修改生产页面：左侧约 30% 为审核，右侧约 70% 保留原聊天；多项审核在左栏切换；窄屏聊天在上、审核在下。

本次修改的是生产 React 页面，不再只是 HTML 原型。

## 2. 修改文件

### web/src/app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx

- 在任务开启 review_enabled 且当前不是 Review 会话时，将聊天主容器改为 grid 分栏。
- 桌面列宽：`minmax(0,3fr) minmax(0,7fr)`。
- 左侧 aside：`ReviewPanel taskId={taskId} queueOnly`。
- 右侧保留原有会话头、消息记录、附件和输入框。
- 小屏使用两行布局，聊天 order=1、审核 order=2。
- 原 Review 会话仍由完整 ReviewPanel 接管，不嵌套分栏。
- 未开启审核的任务保持原布局。

### web/src/app/(main)/function/tasks/detail/_tabs/review-panel.tsx

新增可选 `queueOnly` 模式，复用同一组件已有的数据加载和处置逻辑：

- `api.reviewProposed(taskId)` 每 5 秒获取真实待审意图。
- 数字按钮、上一项、下一项选择意图。
- 显示意图 ID、执行器、优先级、替代关系、summary 和真实预审意见。
- 通过调用 `api.reviewApprove`，携带 content_version 做 CAS。
- 驳回理由必填，调用 `api.reviewReject`；成功后自动显示下一项。
- 无待审意图显示空态。
- 完整 ReviewPanel 的消息、自动处置、intercept 确认和 composer 行为不变。

没有伪造目标、命令或审核数据，没有新增后端 API。

## 3. 自动化验证

| 检查 | 结果 |
|---|---|
| `npx tsc --noEmit` | 通过 |
| `npm run build:static` | 通过，31 个静态页面生成 |
| `git diff --check` | 通过 |
| Docker 构建与健康检查 | 通过 |

日志：

- `/root/artex-review-split-web-build.log`
- `/root/artex-review-split-deploy.log`

备份：

`/root/artex-pre-review-split-20261029-172212.dump`

## 4. 真实 Chrome 验证

使用临时任务 #23 和 3 条临时待审意图，未使用真实目标。

验证结果：

- 左审核、右聊天；宽度比 `0.29998`，符合 30%:70%。
- 3 个数字队列按钮、上一项/下一项正常。
- 通过第一条后待审数从 3 变 2。
- 输入理由驳回下一条后待审数从 2 变 1。
- 右侧原聊天输入框保留。
- 390px 下聊天在上、审核在下，无横向溢出。
- `pageErrors=[]`。
- 总结：`pass=true`。

证据：

- `C:\Users\Administrator\Downloads\artex-docs\artex-review-split-production.png`
- `C:\Users\Administrator\Downloads\artex-docs\artex-review-split-production-actions.png`
- `C:\Users\Administrator\Downloads\artex-docs\artex-review-split-production-mobile.png`
- `C:\Users\Administrator\Downloads\artex-docs\artex-review-split-production-result.json`

## 5. 清理

- 暂停临时任务并取消已派发测试意图。
- 删除任务 #23、其 exploration、临时节点和 74 条测试活动。
- 重启后确认任务 #23 数量为 0，服务 healthy。
- 临时 JWT、HTTP 服务和测试脚本已清理。

## 6. 边界

本次只实现 Review 意图审核分栏。Worker 的 intercept ask 仍在原审批记录/完整 Review 会话中处理，没有混入意图审核队列。
