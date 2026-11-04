/**
 * intercept-tab.tsx：拦截记录面板，包装 ApprovalRecords 展示任务的人工审批记录。
 * 主要函数/类型：
 *   - InterceptTab：按 taskId 渲染 ApprovalRecords 的薄封装。
 */
"use client";

import { ApprovalRecords } from "@/components/approval-records";

export function InterceptTab({ taskId }: { taskId: string }) {
  return <ApprovalRecords key={taskId} taskId={taskId} />;
}
