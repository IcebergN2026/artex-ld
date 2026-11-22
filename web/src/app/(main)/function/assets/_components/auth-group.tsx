/**
 * auth-group.tsx：资产凭据分组组件（发现/认证通过），供资产 Sheet 与内网资产展开详情共用。
 * 主要函数/类型：
 *   - AuthGroup：按 tone 分组的单列凭据卡片列表（含 Secret 明文行）。
 */
"use client";

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import type { AssetAuthItem } from "@/lib/types";
import { cn, fmtTime } from "@/lib/utils";

// AuthGroup 按 tone（discovered=发现 / verified=认证通过）渲染单列凭据卡片：
// 标题、计数与卡片左边线颜色由 tone 决定；列表始终单列纵向排列。
// Secret 明文展示（等宽 + break-all + 明文提示色）：资产 API 与内网视图按已确认
// 需求下发 secret，本组件是唯一展示面，不提供复制/遮罩开关。
export function AuthGroup({
  tone,
  empty,
  items,
  meta,
}: {
  tone: "discovered" | "verified";
  empty: string;
  items: AssetAuthItem[];
  meta: { sourceLabel: string; timeLabel: string };
}) {
  const title = tone === "verified" ? "认证通过" : "发现";
  return (
    <section className="space-y-2">
      <div className="flex items-center gap-2">
        <span
          className={cn("size-2 shrink-0 rounded-full", tone === "verified" ? "bg-emerald-500" : "bg-amber-500")}
          aria-hidden
        />
        <h3 className="text-sm font-medium">{title}</h3>
        <Badge variant="secondary" className="text-[10px] tabular-nums">
          {items.length}
        </Badge>
      </div>
      {items.length === 0 ? (
        <p className="rounded-md border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">{empty}</p>
      ) : (
        <ul className="space-y-2">
          {items.map((item, i) => (
            <li
              key={item.id ?? i}
              className={cn(
                "space-y-1.5 rounded-md border border-l-2 p-3",
                tone === "verified" ? "border-l-emerald-500" : "border-l-amber-500",
              )}
            >
              <div className="flex flex-wrap items-center gap-1.5">
                <Badge variant="outline" className="font-mono text-[10px]">
                  {item.type || "unknown"}
                </Badge>
                <span className="font-mono text-xs font-medium break-all">{item.username || "—"}</span>
                {item.domain ? (
                  <span className="font-mono text-[11px] text-muted-foreground break-all">@{item.domain}</span>
                ) : null}
              </div>
              <dl className="space-y-1 text-xs">
                {item.target ? (
                  <div className="flex gap-2">
                    <dt className="shrink-0 text-muted-foreground">适用服务</dt>
                    <dd className="min-w-0 font-mono break-all">{item.target}</dd>
                  </div>
                ) : null}
                {item.secret ? (
                  <div className="flex gap-2">
                    <dt className="shrink-0 text-muted-foreground">Secret</dt>
                    <dd className="min-w-0 font-mono break-all text-amber-600 dark:text-amber-400">{item.secret}</dd>
                  </div>
                ) : null}
                {item.description ? (
                  <div className="flex gap-2">
                    <dt className="shrink-0 text-muted-foreground">{meta.sourceLabel}</dt>
                    <dd className="min-w-0 break-words whitespace-pre-wrap">{item.description}</dd>
                  </div>
                ) : null}
                {item.last_seen ? (
                  <div className="flex gap-2">
                    <dt className="shrink-0 text-muted-foreground">{meta.timeLabel}</dt>
                    <dd className="tabular-nums text-muted-foreground">{fmtTime(item.last_seen)}</dd>
                  </div>
                ) : null}
              </dl>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
