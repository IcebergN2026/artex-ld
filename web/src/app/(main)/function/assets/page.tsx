/**
 * page.tsx：资产总览页——任务过滤条(溢出自动折叠为任务选择器)+ DSL 搜索 +
 * 「外网资产 / 内网资产」两个主机分区(IP 为基准,行内展开 KV/凭据含 secret/发现,
 * 各自服务端分页)+ 折叠「其他资产(无 IP 的应用/接口)」+ 任务管理抽屉。
 * 主要函数/类型：
 *   - AssetsPage：页面主体,分区各自经 api.hostAssets(intranet 三态)拉取
 *   - SectionPager：分区共享的服务端分页条
 *   - 批量删除:主机行勾选(删该行全部 asset_ids)与其他资产勾选合并经 api.deleteAssets
 */
"use client";

import * as React from "react";

import { ChevronLeftIcon, ChevronRightIcon, ListChecksIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { AssetDslSearch } from "@/components/asset-dsl-search";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { Asset, AssetHostRow, Task } from "@/lib/types";
import { cn, fmtTime } from "@/lib/utils";

import { HostAssets } from "./_components/host-assets";
import { TaskManager } from "./_components/task-manager";
import { TaskTabs } from "./_components/task-tabs";

const PAGE_SIZES = [25, 50, 100, 200];

type HostSection = {
  rows: AssetHostRow[];
  total: number;
  page: number;
  size: number;
  loaded: boolean;
};

const emptySection: HostSection = { rows: [], total: 0, page: 0, size: 50, loaded: false };

export default function AssetsPage() {
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  const [dslError, setDslError] = React.useState("");
  const [taskFilter, setTaskFilter] = React.useState("");
  const [refreshKey, setRefreshKey] = React.useState(0);

  const [ext, setExt] = React.useState<HostSection>(emptySection);
  const [int, setInt] = React.useState<HostSection>(emptySection);
  const [onlyCred, setOnlyCred] = React.useState(false);
  const [extSelectedKey, setExtSelectedKey] = React.useState<string | null>(null);
  const [intSelectedKey, setIntSelectedKey] = React.useState<string | null>(null);

  const [others, setOthers] = React.useState<Asset[]>([]);
  const [othersLoaded, setOthersLoaded] = React.useState(false);
  const [othersCapped, setOthersCapped] = React.useState(false);

  // 主机行勾选(key -> asset_ids 并集)与其他资产勾选合并为一次删除。
  const [hostChecked, setHostChecked] = React.useState<Set<string>>(new Set());
  const [otherChecked, setOtherChecked] = React.useState<Set<number>>(new Set());
  const [deleteIds, setDeleteIds] = React.useState<number[]>([]);
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);
  const [managerOpen, setManagerOpen] = React.useState(false);

  const loadMeta = React.useCallback(() => {
    api.tasks().then((r) => setTasks(r.tasks)).catch(() => {});
    api.assetCounts().then(setCounts).catch(() => {});
  }, []);

  const refresh = React.useCallback(() => {
    loadMeta();
    setRefreshKey((k) => k + 1);
    setHostChecked(new Set());
    setOtherChecked(new Set());
  }, [loadMeta]);

  React.useEffect(() => loadMeta(), [loadMeta]);

  // 任务过滤/搜索变化时各分区回到第一页并收起展开行。
  React.useEffect(() => {
    setExt((s) => ({ ...s, page: 0 }));
    setInt((s) => ({ ...s, page: 0 }));
    setExtSelectedKey(null);
    setIntSelectedKey(null);
    setHostChecked(new Set());
    setOtherChecked(new Set());
  }, [query, taskFilter]);

  const loadSection = React.useCallback(
    (intranet: boolean, section: HostSection, request: { current: number }) => {
      const id = ++request.current;
      const run = async () => {
        try {
          const r = await api.hostAssets(query.trim(), "", section.size, section.page * section.size, taskFilter, intranet);
          if (request.current !== id) return;
          const set = intranet ? setInt : setExt;
          set((s) => ({ ...s, rows: r.hosts, total: r.total, loaded: true }));
          setDslError("");
        } catch (e) {
          if (request.current !== id) return;
          const set = intranet ? setInt : setExt;
          set((s) => ({ ...s, rows: [], total: 0, loaded: true }));
          setDslError(String((e as Error)?.message ?? e));
        }
      };
      const tid = setTimeout(run, query.trim() ? 400 : 0);
      return () => {
        clearTimeout(tid);
        if (request.current === id) request.current++;
      };
    },
    [query, taskFilter],
  );

  const extRequest = React.useRef(0);
  const intRequest = React.useRef(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey 是显式手动刷新触发器。
  React.useEffect(() => loadSection(false, ext, extRequest), [ext.page, ext.size, loadSection, refreshKey]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey 是显式手动刷新触发器。
  React.useEffect(() => loadSection(true, int, intRequest), [int.page, int.size, loadSection, refreshKey]);

  // 其他资产:无 IP 的应用/接口。每类取前 200 条,超出时如实提示用 DSL 收敛。
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey 是显式手动刷新触发器。
  React.useEffect(() => {
    let cancelled = false;
    const run = async () => {
      try {
        const [apps, endpoints] = await Promise.all([
          query.trim()
            ? api.searchAssets(query.trim(), "app", 200, 0, taskFilter)
            : api.assets("app", 200, 0, taskFilter),
          query.trim()
            ? api.searchAssets(query.trim(), "endpoint", 200, 0, taskFilter)
            : api.assets("endpoint", 200, 0, taskFilter),
        ]);
        if (cancelled) return;
        setOthers([...apps.assets, ...endpoints.assets]);
        setOthersCapped(apps.total > 200 || endpoints.total > 200);
        setOthersLoaded(true);
      } catch {
        if (!cancelled) {
          setOthers([]);
          setOthersLoaded(true);
        }
      }
    };
    const tid = setTimeout(run, query.trim() ? 400 : 0);
    return () => {
      cancelled = true;
      clearTimeout(tid);
    };
  }, [query, taskFilter, refreshKey]);

  // 分页越界回退(删除后总数缩水)。
  React.useEffect(() => {
    const last = Math.max(0, Math.ceil(ext.total / ext.size) - 1);
    if (ext.page > last) setExt((s) => ({ ...s, page: last }));
  }, [ext.page, ext.size, ext.total]);
  React.useEffect(() => {
    const last = Math.max(0, Math.ceil(int.total / int.size) - 1);
    if (int.page > last) setInt((s) => ({ ...s, page: last }));
  }, [int.page, int.size, int.total]);

  const totalAssets = Object.values(counts).reduce((sum, n) => sum + n, 0);
  const loading = !ext.loaded || !int.loaded;

  const hostRowByKey = React.useMemo(() => {
    const m = new Map<string, AssetHostRow>();
    for (const row of [...ext.rows, ...int.rows]) m.set(row.key, row);
    return m;
  }, [ext.rows, int.rows]);

  const selectedIds = React.useMemo(() => {
    const ids = new Set<number>(otherChecked);
    for (const key of hostChecked) {
      for (const id of hostRowByKey.get(key)?.asset_ids ?? []) ids.add(id);
    }
    return ids;
  }, [hostChecked, otherChecked, hostRowByKey]);

  const toggleHost = (row: AssetHostRow) =>
    setHostChecked((prev) => {
      const next = new Set(prev);
      if (next.has(row.key)) next.delete(row.key);
      else next.add(row.key);
      return next;
    });
  const toggleHostAll = (rows: AssetHostRow[]) =>
    setHostChecked((prev) => {
      const next = new Set(prev);
      const all = rows.every((row) => next.has(row.key));
      for (const row of rows) {
        if (all) next.delete(row.key);
        else next.add(row.key);
      }
      return next;
    });

  const confirmDelete = async () => {
    setDeleting(true);
    try {
      const res = await api.deleteAssets(deleteIds);
      toast.success(`已删除 ${res.deleted} 条资产`);
      refresh();
    } catch (e) {
      toast.error("删除失败:" + String((e as Error)?.message ?? e));
    } finally {
      setDeleting(false);
      setDeleteOpen(false);
    }
  };

  const intRows = onlyCred ? int.rows.filter((row) => (row.credential_count ?? 0) > 0) : int.rows;

  const selection = { selected: hostChecked, onToggle: toggleHost, onToggleAll: toggleHostAll };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-xl font-semibold tracking-tight">资产</h1>
        <div className="flex items-center gap-3">
          <span className="text-sm text-muted-foreground">
            共 <span className="tabular-nums">{totalAssets}</span> 项资产
          </span>
          {selectedIds.size > 0 && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                setDeleteIds(Array.from(selectedIds));
                setDeleteOpen(true);
              }}
            >
              <Trash2Icon className="size-3.5" /> 删除已选 ({selectedIds.size})
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={refresh} disabled={loading}>
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} /> 刷新
          </Button>
          <Button size="sm" onClick={() => setManagerOpen(true)}>
            <ListChecksIcon className="size-3.5" /> 任务管理
          </Button>
        </div>
      </div>

      <TaskTabs tasks={tasks} value={taskFilter} onChange={setTaskFilter} />

      <AssetDslSearch
        query={query}
        onChange={setQuery}
        loading={loading}
        error={dslError}
        count={query.trim() ? ext.total + int.total : undefined}
      />

      {/* 外网资产 */}
      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <span className="h-4 w-[3px] rounded-full bg-foreground" />
            外网资产
            <span className="font-normal text-muted-foreground">({ext.total} 台主机)</span>
          </h2>
        </div>
        {ext.loaded && ext.rows.length === 0 ? (
          <EmptyHint />
        ) : (
          <HostAssets rows={ext.rows} selectedKey={extSelectedKey} onSelect={setExtSelectedKey} selection={selection} />
        )}
        <SectionPager section={ext} onChange={setExt} />
      </section>

      {/* 内网资产 */}
      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <span className="h-4 w-[3px] rounded-full bg-foreground" />
            内网资产
            <span className="font-normal text-muted-foreground">({int.total} 台主机)</span>
          </h2>
          <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
            <Checkbox checked={onlyCred} onCheckedChange={(c) => setOnlyCred(!!c)} />
            仅显示有凭据
          </label>
        </div>
        {int.loaded && intRows.length === 0 ? (
          <EmptyHint />
        ) : (
          <HostAssets rows={intRows} selectedKey={intSelectedKey} onSelect={setIntSelectedKey} selection={selection} />
        )}
        <SectionPager section={int} onChange={setInt} />
      </section>

      {/* 其他资产 */}
      <Collapsible>
        <Card className="py-0">
          <CollapsibleTrigger className="flex w-full items-center gap-2 px-4 py-3 text-sm font-medium">
            <ChevronRightIcon className="size-4 text-muted-foreground transition-transform [[data-state=open]>&]:rotate-90" />
            其他资产(无 IP 的应用 / 接口)
            <span className="font-normal text-muted-foreground">({others.length})</span>
            {othersCapped && (
              <span className="text-xs font-normal text-muted-foreground">仅显示各类前 200 条,请用 DSL 搜索收敛</span>
            )}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="overflow-x-auto border-t">
              <table className="w-full min-w-[720px] text-sm">
                <thead className="bg-muted/40">
                  <tr className="border-b text-left">
                    <th className="w-8 px-2 py-2.5">
                      <Checkbox
                        checked={
                          others.length > 0 && others.every((a) => otherChecked.has(a.id))
                            ? true
                            : others.some((a) => otherChecked.has(a.id))
                              ? "indeterminate"
                              : false
                        }
                        onCheckedChange={() =>
                          setOtherChecked((prev) => {
                            const all = others.every((a) => prev.has(a.id));
                            const next = new Set(prev);
                            for (const a of others) {
                              if (all) next.delete(a.id);
                              else next.add(a.id);
                            }
                            return next;
                          })
                        }
                      />
                    </th>
                    <th className="px-4 py-2.5 font-medium">类型</th>
                    <th className="px-4 py-2.5 font-medium">名称 / 地址</th>
                    <th className="px-4 py-2.5 font-medium">Bundle ID / 服务</th>
                    <th className="px-4 py-2.5 font-medium">分类 / 状态码</th>
                    <th className="px-4 py-2.5 font-medium">ICP 备案</th>
                    <th className="px-4 py-2.5 font-medium">最近所见</th>
                  </tr>
                </thead>
                <tbody>
                  {others.map((a) => (
                    <tr key={a.id} className={cn("border-b last:border-0", otherChecked.has(a.id) && "bg-muted/40")}>
                      <td className="w-8 px-2 py-2.5">
                        <Checkbox
                          checked={otherChecked.has(a.id)}
                          onCheckedChange={() =>
                            setOtherChecked((prev) => {
                              const next = new Set(prev);
                              if (next.has(a.id)) next.delete(a.id);
                              else next.add(a.id);
                              return next;
                            })
                          }
                          aria-label={`选择资产 ${a.app_name || a.url || a.id}`}
                        />
                      </td>
                      <td className="px-4 py-2.5">
                        <Badge variant={a.type === "app" ? "secondary" : "outline"} className="text-[10px]">
                          {a.type === "app" ? "应用" : "接口"}
                        </Badge>
                      </td>
                      <td className="max-w-[320px] truncate px-4 py-2.5 font-mono text-xs" title={a.app_name || a.url}>
                        {a.app_name || a.url || "—"}
                      </td>
                      <td className="max-w-[220px] truncate px-4 py-2.5 font-mono text-xs">
                        {a.bundle_id || a.service_name || "—"}
                      </td>
                      <td className="px-4 py-2.5 text-xs">{a.category || (a.status_code ? String(a.status_code) : "—")}</td>
                      <td className="px-4 py-2.5 text-xs">{a.app_icp || a.icp || "—"}</td>
                      <td className="px-4 py-2.5 text-xs text-muted-foreground">{fmtTime(a.last_seen)}</td>
                    </tr>
                  ))}
                  {others.length === 0 && (
                    <tr>
                      <td colSpan={7} className="py-10 text-center text-sm text-muted-foreground">
                        {othersLoaded ? "暂无其他资产。" : "加载中…"}
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </CollapsibleContent>
        </Card>
      </Collapsible>

      <TaskManager
        open={managerOpen}
        onOpenChange={setManagerOpen}
        tasks={tasks}
        onChanged={() => {
          refresh();
          if (taskFilter && !tasks.some((t) => t.id === taskFilter)) setTaskFilter("");
        }}
      />

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除 {deleteIds.length} 条资产?</AlertDialogTitle>
            <AlertDialogDescription>资产与其凭据关系将被删除,该操作不可恢复。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={confirmDelete} disabled={deleting}>
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function EmptyHint() {
  return (
    <div className="rounded-md border py-10 text-center text-sm text-muted-foreground">该范围内暂无资产。</div>
  );
}

function SectionPager({
  section,
  onChange,
}: {
  section: HostSection;
  onChange: React.Dispatch<React.SetStateAction<HostSection>>;
}) {
  if (section.total === 0) return null;
  const pageCount = Math.max(1, Math.ceil(section.total / section.size));
  const start = section.page * section.size + 1;
  const end = section.page * section.size + section.rows.length;
  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <Select value={String(section.size)} onValueChange={(v) => onChange((s) => ({ ...s, size: Number(v), page: 0 }))}>
        <SelectTrigger size="sm" className="h-7 w-24">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectGroup>
            {PAGE_SIZES.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n} / 页
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
      <span className="tabular-nums">
        {start}–{end} / {section.total}
      </span>
      {pageCount > 1 && (
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="icon"
            className="size-7"
            disabled={section.page <= 0}
            onClick={() => onChange((s) => ({ ...s, page: Math.max(0, s.page - 1) }))}
          >
            <ChevronLeftIcon />
          </Button>
          <span className="tabular-nums">
            {section.page + 1} / {pageCount}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-7"
            disabled={section.page + 1 >= pageCount}
            onClick={() => onChange((s) => ({ ...s, page: Math.min(pageCount - 1, s.page + 1) }))}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      )}
    </div>
  );
}
