/**
 * page.tsx：拦截审批记录页——仅渲染 ApprovalRecords 组件。
 * 主要函数/类型：
 *   - ApprovalsPage：默认导出页组件
 */
import { ApprovalRecords } from "@/components/approval-records";

export default function ApprovalsPage() {
  return (
    <div className="flex min-w-0 flex-1 flex-col p-4 sm:p-6">
      <ApprovalRecords />
    </div>
  );
}
