/**
 * host-assets.tsx：宿主标签页的资产表格，按企业分组展示主域名/子域名/IP/端口/服务/凭据，
 * 点击 IP 行打开右侧抽屉：域名、端口/服务、凭据汇总与该宿主的发现列表。
 * 主要函数/类型：
 *   - HostAssets：表格主体，选中行后经 api.findingsPage 拉取该宿主资产下的发现
 */
"use client";

import { useEffect, useState } from "react";

import { Globe, KeyRound, Loader2, Network } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { INTRANET_CREDENTIALS_UI } from "@/lib/feature-flags";
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
              <th className="px-4 py-3 font-medium">端口</th>
              <th className="px-4 py-3 font-medium">应用 / 服务 / 接口</th>
              {INTRANET_CREDENTIALS_UI && <th className="px-4 py-3 text-right font-medium">凭据</th>}
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
                  data-open={selectedKey === row.key || undefined}
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
                  <td className="px-4 py-3">
                    <span className="font-mono text-xs">{row.ip}</span>
                    {row.intranet && (
                      <Badge variant="outline" className="ml-2 border-blue-500 text-blue-600">
                        内网
                      </Badge>
                    )}
                  </td>
                  <td className="max-w-[220px] px-4 py-3">
                    {!row.ports || row.ports.length === 0 ? (
                      <span className="text-muted-foreground">—</span>
                    ) : (
                      <div
                        className="truncate font-mono text-xs"
                        title={row.ports.map((p) => `${p.port}/${p.service || "?"}`).join("\n")}
                      >
                        {row.ports
                          .slice(0, 4)
                          .map((p) => `${p.port}/${p.service || "?"}`)
                          .join("  ")}
                        {row.ports.length > 4 && (
                          <span className="ml-2 text-muted-foreground">+{row.ports.length - 4}</span>
                        )}
                      </div>
                    )}
                  </td>
                  <td className="max-w-[360px] px-4 py-3">
                    <div className="truncate" title={row.services.map((service) => service.label).join("\n")}>
                      {row.services[0]?.label || "—"}
                      {row.services.length > 1 && (
                        <span className="ml-2 text-xs text-muted-foreground">+{row.services.length - 1}</span>
                      )}
                    </div>
                  </td>
                  {INTRANET_CREDENTIALS_UI && (
                    <td className="px-4 py-3 text-right">
                      {(row.credential_count ?? 0) === 0 ? (
                        <span className="text-muted-foreground">—</span>
                      ) : (
                        <span className="text-xs">
                          发现 {row.credential_count}
                          {(row.verified_count ?? 0) > 0 && (
                            <span className="text-green-600"> / 已认证 {row.verified_count}</span>
                          )}
                        </span>
                      )}
                    </td>
                  )}
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

      <Sheet
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) onSelect(null);
        }}
      >
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-lg">
          {selected && (
            <>
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2">
                  <Globe className="size-4" />
                  <span className="font-mono">{selected.ip}</span>
                  {selected.intranet && (
                    <Badge variant="outline" className="border-blue-500 text-blue-600">
                      内网
                    </Badge>
                  )}
                </SheetTitle>
                <SheetDescription>
                  {selected.root_domain || "裸 IP"} · {selected.company_name || "未归属"} · {findingTotal} 条发现
                </SheetDescription>
              </SheetHeader>

              <section className="px-4">
                <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                  <Globe className="size-3.5" />
                  域名
                </h3>
                <dl className="mt-2 space-y-2 text-sm">
                  <div className="flex gap-2">
                    <dt className="w-16 shrink-0 text-muted-foreground">主域名</dt>
                    <dd>{selected.root_domain || "—"}</dd>
                  </div>
                  <div className="flex gap-2">
                    <dt className="w-16 shrink-0 text-muted-foreground">子域名</dt>
                    <dd className="flex flex-wrap gap-1">
                      {selected.subdomains.length === 0 ? (
                        <span className="text-muted-foreground">—</span>
                      ) : (
                        selected.subdomains.map((domain) => (
                          <Badge key={domain} variant="secondary" className="font-mono text-xs">
                            {domain}
                          </Badge>
                        ))
                      )}
                    </dd>
                  </div>
                </dl>
              </section>

              <section className="px-4">
                <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                  <Network className="size-3.5" />
                  端口 / 服务
                </h3>
                {(selected.ports?.length ?? 0) === 0 && selected.services.length === 0 ? (
                  <div className="mt-2 text-sm text-muted-foreground">暂无端口与服务。</div>
                ) : (
                  <div className="mt-2 divide-y rounded-md border text-sm">
                    {(selected.ports ?? []).map((p) => (
                      <div key={p.port} className="flex items-center gap-3 px-3 py-2">
                        <span className="w-14 shrink-0 font-mono text-xs font-medium">{p.port}</span>
                        <Badge variant="outline">{p.service || "unknown"}</Badge>
                      </div>
                    ))}
                    {selected.services.map((service) => (
                      <div key={`${service.asset_id}-${service.type}`} className="flex items-center gap-3 px-3 py-2">
                        <Badge variant="secondary">{service.type}</Badge>
                        <span className="truncate font-mono text-xs" title={service.url || service.label}>
                          {service.label}
                        </span>
                        {service.method && <Badge variant="outline">{service.method}</Badge>}
                      </div>
                    ))}
                  </div>
                )}
              </section>

              {INTRANET_CREDENTIALS_UI && (
                <section className="px-4">
                  <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                    <KeyRound className="size-3.5" />
                    凭据
                  </h3>
                  <div className="mt-2 text-sm">
                    {(selected.credential_count ?? 0) === 0 ? (
                      <span className="text-muted-foreground">该宿主暂无凭据。</span>
                    ) : (
                      <span>
                        发现 {selected.credential_count} 条
                        {(selected.verified_count ?? 0) > 0 && (
                          <span className="text-green-600">，其中 {selected.verified_count} 条已认证</span>
                        )}
                        ；明细见「内网凭据」标签页。
                      </span>
                    )}
                  </div>
                </section>
              )}

              <section className="px-4 pb-4">
                <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                  <Globe className="size-3.5" />
                  发现（{findingTotal}）
                </h3>
                {loadingFindings ? (
                  <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
                    <Loader2 className="size-4 animate-spin" />
                    加载发现…
                  </div>
                ) : error ? (
                  <div className="py-4 text-sm text-destructive">{error}</div>
                ) : findings.length === 0 ? (
                  <div className="py-6 text-sm text-muted-foreground">该宿主暂无发现。</div>
                ) : (
                  <div className="mt-2 divide-y rounded-md border">
                    {findings.map((finding) => (
                      <article key={finding.id} className="space-y-1 px-3 py-2">
                        <div className="flex items-center gap-2">
                          <Badge className={severityClass[finding.severity]}>{severityText[finding.severity]}</Badge>
                          <span className="min-w-0 truncate font-medium">{finding.name || finding.vulnclass}</span>
                        </div>
                        <div className="text-xs text-muted-foreground">
                          {finding.vulnclass} · {finding.summary || "暂无摘要"}
                        </div>
                        <div className="text-xs text-muted-foreground">
                          {finding.task_description || `任务 #${finding.task_id || "—"}`}
                        </div>
                      </article>
                    ))}
                  </div>
                )}
              </section>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
