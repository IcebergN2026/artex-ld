/**
 * host-assets.tsx：主机分区的资产表格，按企业分组展示主域名/子域名/IP/端口/服务/凭据，
 * 点击 IP 行在表格下方行内展开（同时只展开一个）：
 * 左侧域名 KV，右侧凭据分组（发现/认证通过，含 secret），中部「服务明细」表
 * （每个端口/服务一行：协议、产品、版本、技术指纹、地址与页面标题/状态码），底部发现列表。
 * 主要函数/类型：
 *   - HostAssets：表格主体，选中行后经 api.searchAssets 取该 IP 凭据、api.findingsPage 拉取发现
 */
"use client";

import { useEffect, useState } from "react";

import { ChevronRight, Loader2 } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { api } from "@/lib/api";
import type { AssetAuthItem, AssetHostRow, Finding, Severity } from "@/lib/types";
import { cn } from "@/lib/utils";

import { AuthGroup } from "./auth-group";

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
  selection,
}: {
  rows: AssetHostRow[];
  selectedKey: string | null;
  onSelect: (key: string | null) => void;
  // selection:提供时渲染勾选列;勾选主机行代表选中其全部 asset_ids(用于批量删除)。
  selection?: {
    selected: Set<string>;
    onToggle: (row: AssetHostRow) => void;
    onToggleAll: (rows: AssetHostRow[]) => void;
  };
}) {
  const selected = rows.find((row) => row.key === selectedKey) ?? null;
  const [findings, setFindings] = useState<Finding[]>([]);
  const [findingTotal, setFindingTotal] = useState(0);
  const [loadingFindings, setLoadingFindings] = useState(false);
  const [error, setError] = useState("");
  const [authItems, setAuthItems] = useState<AssetAuthItem[]>([]);
  const [authLoading, setAuthLoading] = useState(false);

  // 选中行后按 IP 精确取一次资产（AggregateAuth 投影含 secret），供凭据分组展示。
  useEffect(() => {
    if (!selected) {
      setAuthItems([]);
      return;
    }
    let cancelled = false;
    setAuthLoading(true);
    api
      .searchAssets(`ip=${selected.ip}`, "ip", 1, 0)
      .then((r) => {
        if (!cancelled) setAuthItems(r.assets[0]?.auth ?? []);
      })
      .catch(() => {
        if (!cancelled) setAuthItems([]);
      })
      .finally(() => {
        if (!cancelled) setAuthLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [selected]);

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

  const discovered = authItems.filter((item) => item.relation !== "verified");
  const verified = authItems.filter((item) => item.relation === "verified");

  // 服务明细:以端口为基准每端口一行,Web 服务/接口按 port 归并到对应端口行;
  // 无匹配端口的接口单独成行(与 asset-redesign-demo.html 原型同一规则)。
  const detailRows: { port: NonNullable<AssetHostRow["ports"]>[number] | null; svcs: AssetHostRow["services"] }[] =
    selected
      ? [
          ...(selected.ports ?? []).map((port) => ({
            port,
            svcs: selected.services.filter((s) => s.port === port.port),
          })),
          ...selected.services
            .filter((s) => !(selected.ports ?? []).some((p) => p.port === s.port))
            .map((s) => ({ port: null, svcs: [s] })),
        ]
      : [];

  let previousCompany = "";
  return (
    <div className="space-y-4">
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full min-w-[1120px] text-sm">
          <thead className="bg-muted/40">
            <tr className="border-b text-left">
              {selection && (
                <th className="w-8 px-2 py-3" aria-label="全选">
                  <Checkbox
                    checked={
                      rows.length > 0 && rows.every((row) => selection.selected.has(row.key))
                        ? true
                        : rows.some((row) => selection.selected.has(row.key))
                          ? "indeterminate"
                          : false
                    }
                    onCheckedChange={() => selection.onToggleAll(rows)}
                  />
                </th>
              )}
              <th className="w-10 px-2 py-3" aria-label="展开" />
              <th className="px-4 py-3 font-medium">企业</th>
              <th className="px-4 py-3 font-medium">主域名</th>
              <th className="px-4 py-3 font-medium">子域名</th>
              <th className="px-4 py-3 font-medium">IP</th>
              <th className="px-4 py-3 font-medium">端口</th>
              <th className="px-4 py-3 font-medium">应用 / 服务 / 接口</th>
              <th className="px-4 py-3 text-right font-medium">凭据</th>
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
                  aria-expanded={selectedKey === row.key}
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
                  {selection && (
                    <td className="w-8 px-2 py-3" onClick={(event) => event.stopPropagation()}>
                      <Checkbox
                        checked={selection.selected.has(row.key)}
                        onCheckedChange={() => selection.onToggle(row)}
                        aria-label={`选择主机 ${row.ip}`}
                      />
                    </td>
                  )}
                  <td className="w-10 px-2 py-3">
                    <ChevronRight
                      className={cn(
                        "size-4 text-muted-foreground transition-transform",
                        selectedKey === row.key && "rotate-90",
                      )}
                    />
                  </td>
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
                        title={row.ports
                          .map((p) => `${p.port}/${p.service || "?"}${p.product ? ` ${p.product}${p.version ? `/${p.version}` : ""}` : ""}`)
                          .join("\n")}
                      >
                        {row.ports
                          .slice(0, 4)
                          .map((p) => `${p.port}/${p.service || "?"}`)
                          .join("  ")}
                        {row.ports.length > 4 && (
                          <span className="ml-2 text-muted-foreground">+{row.ports.length - 4}</span>
                        )}
                        {row.ports.some((p) => p.product) && (
                          <span className="ml-2 text-[10px] text-muted-foreground">
                            {row.ports.find((p) => p.product)?.product}
                            {row.ports.filter((p) => p.product).length > 1 &&
                              ` +${row.ports.filter((p) => p.product).length - 1}`}
                          </span>
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
        <div className="rounded-md border">
          <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono font-medium">{selected.ip}</span>
              {selected.intranet && (
                <Badge variant="outline" className="border-blue-500 text-blue-600">
                  内网
                </Badge>
              )}
              <span className="text-sm text-muted-foreground">
                {selected.company_name || "未归属"} · {selected.root_domain || "裸 IP"} · {findingTotal} 条发现
              </span>
            </div>
            <Button variant="outline" size="sm" onClick={() => onSelect(null)}>
              收起
            </Button>
          </div>
          <div className="grid gap-6 p-4 md:grid-cols-2">
            <dl className="space-y-2 text-sm">
              <div className="flex gap-3">
                <dt className="w-24 shrink-0 text-muted-foreground">主域名</dt>
                <dd className="min-w-0">{selected.root_domain || "—"}</dd>
              </div>
              <div className="flex gap-3">
                <dt className="w-24 shrink-0 text-muted-foreground">子域名</dt>
                <dd className="flex min-w-0 flex-wrap gap-1">
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
            <div className="space-y-4">
              {authLoading ? (
                <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
                  <Loader2 className="size-4 animate-spin" />
                  加载凭据…
                </div>
              ) : (
                <>
                  <AuthGroup
                    tone="discovered"
                    empty="暂无"
                    items={discovered}
                    meta={{ sourceLabel: "来源", timeLabel: "最近发现" }}
                  />
                  <AuthGroup
                    tone="verified"
                    empty="暂无"
                    items={verified}
                    meta={{ sourceLabel: "证据", timeLabel: "认证时间" }}
                  />
                </>
              )}
            </div>
          </div>
          <div className="border-t p-4">
            <h3 className="text-xs font-medium text-muted-foreground">服务明细（{detailRows.length}）</h3>
            {detailRows.length === 0 ? (
              <div className="py-6 text-sm text-muted-foreground">该主机暂无已识别服务。</div>
            ) : (
              <div className="mt-2 overflow-x-auto rounded-md border">
                <table className="w-full text-xs">
                  <thead className="bg-muted/40">
                    <tr className="border-b text-left">
                      <th className="px-3 py-2 font-medium">端口/协议</th>
                      <th className="px-3 py-2 font-medium">产品</th>
                      <th className="px-3 py-2 font-medium">版本</th>
                      <th className="px-3 py-2 font-medium">技术指纹</th>
                      <th className="px-3 py-2 font-medium">地址 / 标题</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detailRows.map(({ port, svcs }, i) => {
                      const tech = Array.from(new Set(svcs.flatMap((s) => s.technologies ?? [])));
                      return (
                        <tr key={port?.port ?? `svc-${i}`} className="border-b last:border-0">
                          <td className="px-3 py-2 font-mono whitespace-nowrap">
                            {port ? `${port.port}/${port.service || "?"}` : "—"}
                          </td>
                          <td className="px-3 py-2">{port?.product || <span className="text-muted-foreground">—</span>}</td>
                          <td className="px-3 py-2 font-mono">{port?.version || <span className="text-muted-foreground">—</span>}</td>
                          <td className="px-3 py-2">
                            {tech.length === 0 ? (
                              <span className="text-muted-foreground">—</span>
                            ) : (
                              <div className="flex flex-wrap gap-1">
                                {tech.map((t) => (
                                  <Badge
                                    key={t}
                                    variant="outline"
                                    className="border-violet-500/40 bg-violet-500/10 font-mono text-[10px] text-violet-600"
                                  >
                                    {t}
                                  </Badge>
                                ))}
                              </div>
                            )}
                          </td>
                          <td className="max-w-[360px] px-3 py-2">
                            {svcs.length === 0 ? (
                              <span className="text-muted-foreground">—</span>
                            ) : (
                              svcs.map((s) => (
                                <div key={`${s.asset_id}-${s.type}`} className="flex flex-wrap items-center gap-2 py-0.5">
                                  <span className="truncate font-mono" title={s.url || s.label}>
                                    {s.label}
                                  </span>
                                  {s.page_title && (
                                    <span className="truncate text-muted-foreground">{s.page_title}</span>
                                  )}
                                  {s.status_code != null && (
                                    <Badge variant="outline" className="font-mono text-[10px]">
                                      {s.status_code}
                                    </Badge>
                                  )}
                                </div>
                              ))
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
          <div className="border-t p-4">
            <h3 className="text-xs font-medium text-muted-foreground">发现（{findingTotal}）</h3>
            {loadingFindings ? (
              <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
                加载发现…
              </div>
            ) : error ? (
              <div className="py-4 text-sm text-destructive">{error}</div>
            ) : findings.length === 0 ? (
              <div className="py-6 text-sm text-muted-foreground">该主机暂无发现。</div>
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
          </div>
        </div>
      )}
    </div>
  );
}
