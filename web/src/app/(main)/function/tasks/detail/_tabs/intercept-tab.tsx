/**
 * intercept-tab.tsx：拦截记录面板，包装 ApprovalRecords 展示任务的人工审批记录，
 * 并在下方附任务的 Guard 审计记录(planner Bash 拦截等平台管控的落点)。
 * 主要函数/类型：
 *   - InterceptTab：按 taskId 渲染 ApprovalRecords + GuardAuditCard。
 *   - GuardAuditCard：任务级 Guard 审计(allow/block,含 planner 内置规则 block)。
 */
"use client";

import * as React from "react";
import { ShieldAlertIcon } from "lucide-react";

import { ApprovalRecords } from "@/components/approval-records";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { AuditEntry } from "@/lib/types";

// fmtAuditTs 兼容两种 ts 形态:服务端实际下发 Unix 秒(int64),类型声明为 string。
function fmtAuditTs(ts: string): string {
  const n = Number(ts);
  const d = Number.isFinite(n) && n > 0 ? new Date(n * 1000) : new Date(ts);
  if (Number.isNaN(d.getTime())) return String(ts);
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// auditReason 提取 RuleID([planner_xxx])与展示文本。完整 reason 形如
// 【artex 平台管控·非目标防御】…原因：[planner_bash_network_tool] …,RuleID 不在行首。
function splitReason(reason?: string): { ruleId: string; text: string } {
  if (!reason) return { ruleId: "", text: "" };
  const m = reason.match(/\[(planner_[a-z0-9_]+)\]/);
  if (m) return { ruleId: m[1], text: reason };
  return { ruleId: "", text: reason };
}

function GuardAuditCard({ taskId }: { taskId: string }) {
  const [entries, setEntries] = React.useState<AuditEntry[]>([]);
  const [loaded, setLoaded] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    setLoaded(false);
    api
      .audit(taskId)
      .then((a) => {
        if (!alive) return;
        setEntries(a.entries ?? []);
        setLoaded(true);
      })
      .catch(() => {
        if (!alive) return;
        setEntries([]);
        setLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, [taskId]);

  return (
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center gap-2 border-b px-4 py-2.5">
          <ShieldAlertIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
          <p className="text-sm font-semibold">Guard 审计</p>
          <span className="text-xs text-muted-foreground">
            本任务内经 Guard 的工具调用决策(含 planner 内置规则拦截,先于上方审批记录)
          </span>
          <span className="flex-1" />
          <span className="text-xs text-muted-foreground">{loaded ? `${entries.length} 条` : "加载中…"}</span>
        </div>
        {loaded && entries.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">
            本任务暂无 Guard 审计记录(未配置 Guard 或尚无被拦截/记录的调用)
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-[140px]">时间</TableHead>
                <TableHead className="w-[110px]">工具</TableHead>
                <TableHead className="w-[64px]">决策</TableHead>
                <TableHead className="w-[230px]">RuleID</TableHead>
                <TableHead>原因 / 命令</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {[...entries].reverse().map((e, i) => {
                const { ruleId, text } = splitReason(e.reason);
                return (
                  <TableRow key={`${e.ts}-${i}`}>
                    <TableCell className="font-mono text-xs tabular-nums">{fmtAuditTs(e.ts)}</TableCell>
                    <TableCell>
                      <code className="text-xs">{e.tool}</code>
                    </TableCell>
                    <TableCell>
                      {e.action === "block" ? (
                        <Badge variant="destructive">block</Badge>
                      ) : (
                        <Badge variant="secondary">allow</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      {ruleId ? <code className="text-xs text-muted-foreground">{ruleId}</code> : "—"}
                    </TableCell>
                    <TableCell className="max-w-[320px]">
                      <span className="block truncate text-xs" title={text}>
                        {text || "—"}
                      </span>
                      {e.command && (
                        <code
                          className="mt-0.5 block truncate rounded bg-muted px-1.5 py-0.5 text-[11px] font-mono"
                          title={e.command}
                        >
                          {e.command}
                        </code>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

export function InterceptTab({ taskId }: { taskId: string }) {
  return (
    <div className="flex flex-col gap-4">
      <ApprovalRecords key={taskId} taskId={taskId} />
      <GuardAuditCard taskId={taskId} />
    </div>
  );
}
