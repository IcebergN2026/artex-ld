"use client";

import { useEffect, useState } from "react";

import { Globe, Loader2, RotateCcw } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { AssetHostRow, Finding, Severity } from "@/lib/types";

const severityText: Record<Severity, string> = { critical: "严重", high: "高危", medium: "中危", low: "低危" };
const severityClass: Record<Severity, string> = {
  critical: "bg-red-600 text-white",
  high: "bg-orange-500 text-white",
  medium: "bg-yellow-500 text-black",
  low: "bg-slate-500 text-white",
};

export function HostAssets({
  rows,
  selectedKey,
  onSelect,
}: {
  rows: AssetHostRow[];
  selectedKey: string | null;
  onSelect: (key: string | null) => void;
}) {
  const selected = rows.find((row) => row.key === selectedKey) ?? null;
  const [findings, setFindings] = useState<Finding[]>([]);
  const [findingTotal, setFindingTotal] = useState(0);
  const [loadingFindings, setLoadingFindings] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!selected) {
      setFindings([]);
      setFindingTotal(0);
      return;
    }
    let cancelled = false;
    setLoadingFindings(true);
    setError("");
    api
      .findingsPage({ page: 1, pageSize: 50, sort: "severity", assetIDs: selected.asset_ids })
      .then((result) => {
        if (cancelled) return;
        setFindings(result.items ?? []);
        setFindingTotal(result.total ?? 0);
      })
      .catch((err) => !cancelled && setError(err instanceof Error ? err.message : String(err)))
      .finally(() => !cancelled && setLoadingFindings(false));
    return () => {
      cancelled = true;
    };
  }, [selected]);

  let previousCompany = "";
  return (
    <div className="space-y-4">
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full min-w-[1120px] text-sm">
          <thead className="bg-muted/40">
            <tr className="border-b text-left">
              <th className="px-4 py-3 font-medium">企业</th>
              <th className="px-4 py-3 font-medium">主域名</th>
              <th className="px-4 py-3 font-medium">子域名</th>
              <th className="px-4 py-3 font-medium">IP</th>
              <th className="px-4 py-3 font-medium">应用 / 服务 / 接口</th>
              <th className="px-4 py-3 text-right font-medium">发现数</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const company = row.company_name || "未归属";
              const showCompany = company !== previousCompany;
              previousCompany = company;
              const primarySubdomain = row.subdomains[0] ?? "";
              return (
                <tr
                  key={row.key}
                  tabIndex={0}
                  aria-pressed={selectedKey === row.key}
                  className={`cursor-pointer border-b outline-none transition-colors last:border-0 focus-visible:ring-2 focus-visible:ring-ring ${selectedKey === row.key ? "bg-muted" : "hover:bg-muted/40"}`}
                  onClick={() => onSelect(selectedKey === row.key ? null : row.key)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      onSelect(selectedKey === row.key ? null : row.key);
                    }
                  }}
                >
                  <td className="px-4 py-3 font-medium">{showCompany ? company : ""}</td>
                  <td className="px-4 py-3">{row.root_domain || "—"}</td>
                  <td className="px-4 py-3">
                    {primarySubdomain || "—"}
                    {row.subdomains.length > 1 && (
                      <span className="ml-2 text-xs text-muted-foreground" title={row.subdomains.slice(1).join("\n")}>
                        +{row.subdomains.length - 1} 其他
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs">{row.ip}</td>
                  <td className="max-w-[360px] px-4 py-3">
                    <div className="truncate" title={row.services.map((service) => service.label).join("\n")}>
                      {row.services[0]?.label || "—"}
                      {row.services.length > 1 && (
                        <span className="ml-2 text-xs text-muted-foreground">+{row.services.length - 1}</span>
                      )}
                    </div>
                  </td>
                  <td className="px-4 py-3 text-right">
                    <div className="flex items-center justify-end gap-1">
                      {row.critical > 0 && <Badge className="bg-red-600 text-white">严 {row.critical}</Badge>}
                      {row.high > 0 && <Badge className="bg-orange-500 text-white">高 {row.high}</Badge>}
                      {row.medium > 0 && <Badge className="bg-yellow-500 text-black">中 {row.medium}</Badge>}
                      {row.low > 0 && <Badge className="bg-slate-500 text-white">低 {row.low}</Badge>}
                      <span className="ml-1 font-medium">{row.finding_count}</span>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {selected && (
        <section className="rounded-md border">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
            <div>
              <h2 className="flex items-center gap-2 text-sm font-medium">
                <Globe className="size-4" />
                {selected.ip} 的发现
              </h2>
              <p className="mt-1 text-xs text-muted-foreground">
                {selected.root_domain || "裸 IP"} · {findingTotal} 条
              </p>
            </div>
            <Button variant="ghost" size="sm" onClick={() => onSelect(null)}>
              <RotateCcw className="size-4" />
              取消筛选
            </Button>
          </div>
          {loadingFindings ? (
            <div className="flex items-center gap-2 p-6 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              加载发现…
            </div>
          ) : error ? (
            <div className="p-4 text-sm text-destructive">{error}</div>
          ) : findings.length === 0 ? (
            <div className="p-6 text-sm text-muted-foreground">该宿主暂无发现。</div>
          ) : (
            <div className="divide-y">
              {findings.map((finding) => (
                <article key={finding.id} className="grid gap-2 px-4 py-3 md:grid-cols-[auto_1fr_auto] md:items-start">
                  <Badge className={severityClass[finding.severity]}>{severityText[finding.severity]}</Badge>
                  <div className="min-w-0">
                    <div className="font-medium">{finding.name || finding.vulnclass}</div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {finding.vulnclass} · {finding.summary || "暂无摘要"}
                    </div>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {finding.task_description || `任务 #${finding.task_id || "—"}`}
                  </span>
                </article>
              ))}
            </div>
          )}
        </section>
      )}
    </div>
  );
}
