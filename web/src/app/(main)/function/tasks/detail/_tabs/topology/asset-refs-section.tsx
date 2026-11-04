/**
 * asset-refs-section.tsx：资产关联数据（意图/事实/发现）的获取 hooks 与展示组件。
 * 主要函数/类型：
 *   - useAssetRefs / useIPAssetRefs：按 asset_id / IP 拉取任务内资产关联数据
 *   - AssetRefsSection：意图/事实/发现三段展示，无数据整段不渲染
 *   - RawJSON：折叠展示的原始 JSON 块
 */
"use client";

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { api } from "@/lib/api";
import type { Asset, CoverageAssetRef, CoverageAssetRefs } from "@/lib/types";

/**
 * 资产在本任务里关联的意图/事实/发现。
 * 覆盖图抽屉与拓扑节点抽屉共用这段数据获取逻辑，避免两处写法漂移。
 */
export function useAssetRefs(taskId: string, assetId: number | null | undefined): CoverageAssetRefs | null {
  const [refs, setRefs] = React.useState<CoverageAssetRefs | null>(null);

  React.useEffect(() => {
    setRefs(null);
    if (!assetId) return;
    let cancelled = false;
    api
      .taskAssetRefs(taskId, assetId)
      .then((r) => {
        if (!cancelled) setRefs(r);
      })
      .catch(() => {
        /* 无关联或出错：不展示该区块 */
      });
    return () => {
      cancelled = true;
    };
  }, [taskId, assetId]);

  return refs;
}

/**
 * 拓扑节点只有 IP，没有 asset_id。先按 `ip==…` 在任务资产里找对应资产，再取关联数据。
 * 找不到（或资产不在本任务）时返回 null，抽屉只展示原始图数据。
 */
export function useIPAssetRefs(taskId: string, ip: string | null): CoverageAssetRefs | null {
  const [assetId, setAssetId] = React.useState<number | null>(null);

  React.useEffect(() => {
    setAssetId(null);
    if (!ip) return;
    let cancelled = false;
    api
      .searchTaskAssets(taskId, `ip==${ip}`, "ip", 1, 0)
      .then((r) => {
        if (cancelled) return;
        const hit = (r.assets ?? []).find((a: Asset) => a.ip === ip) ?? (r.assets ?? [])[0];
        setAssetId(hit?.id ?? null);
      })
      .catch(() => {
        /* 忽略：无对应资产 */
      });
    return () => {
      cancelled = true;
    };
  }, [taskId, ip]);

  return useAssetRefs(taskId, assetId);
}

function RefList({ title, items }: { title: string; items: CoverageAssetRef[] }) {
  if (items.length === 0) return null;
  return (
    <div>
      <h4 className="text-muted-foreground mb-1 text-xs font-medium">
        {title}（{items.length}）
      </h4>
      <div className="flex flex-col gap-1">
        {items.map((r) => (
          <div
            key={`${r.kind}-${r.id}`}
            className="bg-muted/50 flex flex-wrap items-start gap-2 rounded-md px-2 py-1.5 text-xs"
          >
            <span className="text-muted-foreground shrink-0 font-mono">#{r.id}</span>
            {r.state && <span className="text-muted-foreground shrink-0">{r.state}</span>}
            <span className="text-foreground min-w-32 flex-1 break-words">{r.summary || "—"}</span>
            {r.inherited && r.source_task_id && (
              <Badge variant="outline" className="shrink-0">
                来源 #{r.source_task_id} · 只读
              </Badge>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

/** 关联意图/事实/发现三段；没有数据时整段不渲染。 */
export function AssetRefsSection({ refs }: { refs: CoverageAssetRefs | null }) {
  if (!refs) return null;
  if (refs.intents.length === 0 && refs.facts.length === 0 && refs.findings.length === 0) return null;
  return (
    <div className="flex flex-col gap-3">
      <RefList title="关联意图" items={refs.intents} />
      <RefList title="关联事实" items={refs.facts} />
      <RefList title="关联发现" items={refs.findings} />
    </div>
  );
}

/** 折叠展示的原始 JSON 块。 */
export function RawJSON({ value }: { value: unknown }) {
  return (
    <pre className="bg-muted/50 text-foreground max-w-full overflow-hidden rounded-md border p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}
