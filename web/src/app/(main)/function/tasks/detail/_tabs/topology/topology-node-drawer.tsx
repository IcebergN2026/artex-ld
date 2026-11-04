/**
 * topology-node-drawer.tsx：拓扑节点详情抽屉（属性编辑、入边管理、备注、资产关联）。
 * 主要函数/类型：
 *   - TopologyNodeDrawer：节点详情抽屉主组件
 *   - ColorPicker / COLOR_PRESETS：节点/连线共用的颜色选择器与 8 个预设色
 *   - DetailRow：标签-内容展示行（供其他面板复用）
 */
"use client";

import * as React from "react";

import { ChevronDown, Plus, Save, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { TopologyEdgeKind, TopologyGraph, TopologyGraphEdge, TopologyGraphNode } from "@/lib/types";
import { cn } from "@/lib/utils";

import { AssetRefsSection, RawJSON, useIPAssetRefs } from "./asset-refs-section";
import { EDGE_KIND_META, EDGE_KIND_OPTIONS, ENTRY_STATUS_LABEL, nodeTitle } from "./graph-model";

function errorText(reason: unknown): string {
  if (reason instanceof Error) return reason.message;
  return String(reason);
}

function formatTime(value: string): string {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString();
}

/** 入口状态文字颜色；未登记状态回退到中性色。 */
const ENTRY_TEXT_TONE: Record<string, string> = {
  confirmed: "text-emerald-600 dark:text-emerald-400",
  suspected: "text-amber-600 dark:text-amber-400",
};

/** 颜色预设：覆盖常见标注需求，配合原生取色器与「默认」清除。 */
export const COLOR_PRESETS = ["#1d4ed8", "#0e7490", "#047857", "#65a30d", "#b45309", "#dc2626", "#7c3aed", "#be185d"];

/**
 * 节点/联系共用的颜色选择器：8 个预设色块 + 原生取色器 + 「默认」清除（""）。
 * 导出给 topology-edge-dialog 复用，避免新增文件。
 */
export function ColorPicker({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (color: string) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {COLOR_PRESETS.map((preset) => (
        <button
          key={preset}
          type="button"
          aria-label={`颜色 ${preset}`}
          disabled={disabled}
          onClick={() => onChange(preset)}
          className={cn(
            "size-5 rounded-full border border-black/10 transition-shadow disabled:cursor-not-allowed disabled:opacity-50",
            value === preset && "ring-2 ring-blue-500 ring-offset-1 ring-offset-background",
          )}
          style={{ backgroundColor: preset }}
        />
      ))}
      <label
        className="relative flex h-5 cursor-pointer items-center overflow-hidden rounded border border-black/10 disabled:cursor-not-allowed"
        title="自定义颜色"
      >
        <input
          type="color"
          value={value || "#666666"}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
          className="h-5 w-7 cursor-pointer disabled:cursor-not-allowed"
        />
      </label>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        disabled={!value || disabled}
        onClick={() => onChange("")}
        className="h-6 px-2 text-xs"
      >
        默认
      </Button>
    </div>
  );
}

export function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[72px_1fr] items-start gap-2 py-0.5 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words">{children}</span>
    </div>
  );
}

function EditableTextRow({
  label,
  value,
  pending,
  onSave,
}: {
  label: string;
  value: string;
  pending: boolean;
  onSave: (next: string) => Promise<void>;
}) {
  const [draft, setDraft] = React.useState(value);
  const dirty = draft !== value;
  React.useEffect(() => setDraft(value), [value]);
  return (
    <DetailRow label={label}>
      <div className="flex items-center gap-1.5">
        <Input value={draft} maxLength={255} onChange={(e) => setDraft(e.target.value)} disabled={pending} />
        <Button variant="outline" size="sm" disabled={pending || !dirty} onClick={() => void onSave(draft.trim())}>
          <Save className="size-3.5" />
          保存
        </Button>
      </div>
    </DetailRow>
  );
}

function EditableDescriptionRow({
  value,
  pending,
  onSave,
}: {
  value: string;
  pending: boolean;
  onSave: (next: string) => Promise<void>;
}) {
  const [draft, setDraft] = React.useState(value);
  const dirty = draft !== value;
  React.useEffect(() => setDraft(value), [value]);
  return (
    <DetailRow label="节点描述">
      <div className="flex flex-col gap-1.5">
        <textarea
          className="min-h-16 w-full rounded-md border border-input bg-transparent px-2 py-1 text-sm outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50"
          value={draft}
          maxLength={500}
          placeholder="这个资产是什么（服务/端口/系统/容器…），留空不显示"
          onChange={(e) => setDraft(e.target.value)}
          disabled={pending}
        />
        <Button
          variant="outline"
          size="sm"
          className="self-end"
          disabled={pending || !dirty}
          onClick={() => void onSave(draft.trim())}
        >
          <Save className="size-3.5" />
          保存
        </Button>
      </div>
    </DetailRow>
  );
}

function KindSelect({
  value,
  onValueChange,
  className,
}: {
  value: TopologyEdgeKind;
  onValueChange: (kind: TopologyEdgeKind) => void;
  className?: string;
}) {
  return (
    <Select value={value} onValueChange={(v) => onValueChange(v as TopologyEdgeKind)}>
      <SelectTrigger size="sm" className={cn("w-28", className)}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {EDGE_KIND_OPTIONS.map((opt) => (
          <SelectItem key={opt.value} value={opt.value}>
            {opt.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function IncomingEdgeRow({
  edge,
  disabled,
  onSave,
  onDelete,
}: {
  edge: TopologyGraphEdge;
  disabled: boolean;
  onSave: (edge: TopologyGraphEdge, patch: { label: string; kind: TopologyEdgeKind; color: string }) => void;
  onDelete: (edge: TopologyGraphEdge) => void;
}) {
  const [kind, setKind] = React.useState<TopologyEdgeKind>(edge.kind);
  const [label, setLabel] = React.useState(edge.label);
  const [color, setColor] = React.useState(edge.color);
  React.useEffect(() => {
    setKind(edge.kind);
    setLabel(edge.label);
    setColor(edge.color);
  }, [edge.kind, edge.label, edge.color]);
  const dirty = kind !== edge.kind || label !== edge.label || color !== edge.color;

  return (
    <div className="flex flex-col gap-2 rounded-md border p-2.5">
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground text-[11px]">来源</span>
        <span className="truncate font-mono text-xs">{edge.src_ip}</span>
        <span className={cn("ml-auto rounded border px-1.5 py-0.5 text-[10px]", EDGE_KIND_META[kind].chip)}>
          {EDGE_KIND_META[kind].label}
        </span>
      </div>
      <div className="flex items-center gap-2">
        <KindSelect value={kind} onValueChange={setKind} />
        <input
          value={label}
          maxLength={200}
          placeholder="联系说明，例如：通过共享运维终端访问"
          onChange={(e) => setLabel(e.target.value)}
          className="border-input h-8 min-w-0 flex-1 rounded-md border bg-transparent px-2 text-xs outline-none focus-visible:border-blue-400"
        />
      </div>
      <div className="flex items-center gap-2">
        <span className="text-[11px] text-muted-foreground">颜色</span>
        <ColorPicker value={color} onChange={setColor} disabled={disabled} />
      </div>
      <div className="flex items-center gap-2">
        {edge.evidence_ref ? (
          <Badge variant="outline" className="font-mono text-[10px]">
            {edge.evidence_ref}
          </Badge>
        ) : (
          <span className="text-muted-foreground text-[10px]">无证据引用</span>
        )}
        <div className="ml-auto flex items-center gap-1.5">
          <Button
            size="sm"
            variant="outline"
            disabled={disabled || !dirty}
            onClick={() => onSave(edge, { label, kind, color })}
          >
            <Save className="size-3.5" />
            保存
          </Button>
          <Button size="sm" variant="ghost" disabled={disabled} onClick={() => onDelete(edge)}>
            <Trash2 className="size-3.5" />
            删除
          </Button>
        </div>
      </div>
    </div>
  );
}

function AddEdgeForm({
  node,
  graph,
  disabled,
  onSubmit,
}: {
  node: TopologyGraphNode;
  graph: TopologyGraph;
  disabled: boolean;
  onSubmit: (edge: { src_ip: string; kind: TopologyEdgeKind; label: string }) => void;
}) {
  const others = graph.nodes.filter((n) => n.ip !== node.ip);
  const [src, setSrc] = React.useState(others[0]?.ip ?? "");
  const [kind, setKind] = React.useState<TopologyEdgeKind>("solid");
  const [label, setLabel] = React.useState("");

  if (others.length === 0) {
    return <p className="text-muted-foreground text-xs">图中只有这一个节点，先添加更多节点再建立联系。</p>;
  }

  return (
    <div className="bg-muted/40 flex flex-col gap-2 rounded-md border p-2.5">
      <div className="flex items-center gap-2">
        <Select value={src} onValueChange={setSrc}>
          <SelectTrigger size="sm" className="min-w-0 flex-1">
            <SelectValue placeholder="来源节点" />
          </SelectTrigger>
          <SelectContent>
            {others.map((n) => (
              <SelectItem key={n.ip} value={n.ip}>
                {nodeTitle(n)} · {n.ip}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <KindSelect value={kind} onValueChange={setKind} />
      </div>
      <div className="flex items-center gap-2">
        <input
          value={label}
          maxLength={200}
          placeholder="联系说明（可选）"
          onChange={(e) => setLabel(e.target.value)}
          className="border-input h-8 min-w-0 flex-1 rounded-md border bg-transparent px-2 text-xs outline-none focus-visible:border-blue-400"
        />
        <Button size="sm" disabled={disabled || !src} onClick={() => onSubmit({ src_ip: src, kind, label })}>
          <Plus className="size-3.5" />
          添加
        </Button>
      </div>
    </div>
  );
}

export function TopologyNodeDrawer({
  taskId,
  graph,
  ip,
  onGraph,
  onClose,
  onDeleted,
}: {
  taskId: string;
  graph: TopologyGraph;
  ip: string | null;
  onGraph: (graph: TopologyGraph) => void;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const node = ip ? (graph.nodes.find((n) => n.ip === ip) ?? null) : null;
  const refs = useIPAssetRefs(taskId, node?.ip ?? null);
  const [pending, setPending] = React.useState(false);
  const [note, setNote] = React.useState("");
  const [rawOpen, setRawOpen] = React.useState(false);

  // Selecting another node resets the note draft and the raw-data fold.
  // biome-ignore lint/correctness/useExhaustiveDependencies: selecting another node intentionally resets the note draft.
  React.useEffect(() => {
    setNote("");
    setRawOpen(false);
  }, [ip]);

  const incoming = node ? graph.edges.filter((e) => e.dst_ip === node.ip) : [];
  const group = node?.group_id != null ? graph.groups.find((g) => g.id === node.group_id) : undefined;
  const notes = node ? (graph.notes[node.ip] ?? []) : [];

  async function run(task: () => Promise<TopologyGraph>, success?: string) {
    setPending(true);
    try {
      const next = await task();
      onGraph(next);
      if (success) toast.success(success);
      return next;
    } catch (reason) {
      toast.error(`操作失败：${errorText(reason)}`);
      return null;
    } finally {
      setPending(false);
    }
  }

  return (
    <Sheet
      open={node !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <SheetContent className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-xl">
        {node && (
          <>
            <SheetHeader className="border-b p-4">
              <SheetTitle className="pr-8 leading-tight">{nodeTitle(node)}</SheetTitle>
              <SheetDescription className="font-mono text-xs">
                {node.ip}
                {node.display_label ? ` · ${node.display_label}` : ""}
              </SheetDescription>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-5 p-4">
                <section>
                  <Collapsible open={rawOpen} onOpenChange={setRawOpen}>
                    <div className="mb-1 flex items-center justify-between">
                      <h3 className="text-sm font-semibold">原始数据</h3>
                      <CollapsibleTrigger asChild>
                        <Button size="sm" variant="ghost">
                          {rawOpen ? "收起" : "展开"}
                          <ChevronDown className={cn("size-3.5 transition-transform", rawOpen && "rotate-180")} />
                        </Button>
                      </CollapsibleTrigger>
                    </div>
                    <CollapsibleContent className="flex flex-col gap-3">
                      <RawJSON value={node} />
                      <AssetRefsSection refs={refs} />
                      {!refs && (
                        <p className="text-muted-foreground text-xs">该 IP 未在当前任务资产中登记，无关联意图/发现。</p>
                      )}
                    </CollapsibleContent>
                  </Collapsible>
                </section>

                <section>
                  <h3 className="mb-1 text-sm font-semibold">机器信息</h3>
                  <DetailRow label="IP">{node.ip}</DetailRow>
                  <EditableTextRow
                    label="主机名"
                    value={node.hostname}
                    pending={pending}
                    onSave={async (next) => {
                      await run(async () => api.patchTopologyNode(taskId, node.ip, { hostname: next }), "主机名已保存");
                    }}
                  />
                  <EditableTextRow
                    label="显示名"
                    value={node.display_label}
                    pending={pending}
                    onSave={async (next) => {
                      await run(
                        async () => api.patchTopologyNode(taskId, node.ip, { display_label: next }),
                        "显示名已保存",
                      );
                    }}
                  />
                  <EditableDescriptionRow
                    value={node.description ?? ""}
                    pending={pending}
                    onSave={async (next) => {
                      await run(
                        async () => api.patchTopologyNode(taskId, node.ip, { description: next }),
                        "节点描述已保存",
                      );
                    }}
                  />
                  <DetailRow label="入口状态">
                    <span className={cn(ENTRY_TEXT_TONE[node.entry_status] ?? "text-muted-foreground")}>
                      {ENTRY_STATUS_LABEL[node.entry_status] ?? "非入口"}
                    </span>
                  </DetailRow>
                  <DetailRow label="分组">{group?.name ?? "未分组"}</DetailRow>
                  <DetailRow label="颜色">
                    <div className="flex items-center gap-2">
                      {node.color ? (
                        <span
                          className="size-3.5 shrink-0 rounded-full border border-black/10"
                          style={{ backgroundColor: node.color }}
                        />
                      ) : (
                        <span className="text-muted-foreground">默认</span>
                      )}
                      <ColorPicker
                        value={node.color}
                        disabled={pending}
                        onChange={(color) =>
                          void run(
                            () => api.patchTopologyNode(taskId, node.ip, { color }),
                            color ? "颜色已更新" : "已恢复默认颜色",
                          )
                        }
                      />
                    </div>
                  </DetailRow>
                  {node.entry_status === "suspected" && (
                    <div className="mt-2 flex items-center gap-2 rounded-md border border-amber-400/50 bg-amber-50/60 p-2 dark:bg-amber-950/40">
                      <p className="text-muted-foreground flex-1 text-xs">
                        该节点疑似入口。确认后后端会把其它节点降级为普通主机。
                      </p>
                      <Button
                        size="sm"
                        disabled={pending}
                        onClick={() =>
                          void run(
                            () => api.patchTopologyNode(taskId, node.ip, { entry_status: "confirmed" }),
                            "已标记为入口",
                          )
                        }
                      >
                        确认入口
                      </Button>
                    </div>
                  )}
                </section>

                <section className="flex flex-col gap-2">
                  <h3 className="text-sm font-semibold">
                    发现关系
                    <span className="text-muted-foreground ml-1.5 text-xs font-normal">
                      指向该节点的 {incoming.length} 条
                    </span>
                  </h3>
                  {incoming.length === 0 && <p className="text-muted-foreground text-xs">还没有其他节点指向它。</p>}
                  {incoming.map((edge) => (
                    <IncomingEdgeRow
                      key={edge.id}
                      edge={edge}
                      disabled={pending}
                      onSave={(target, patch) =>
                        void run(() => api.patchTopologyEdge(taskId, target.id, patch), "联系已更新")
                      }
                      onDelete={(target) => void run(() => api.deleteTopologyEdge(taskId, target.id), "联系已删除")}
                    />
                  ))}
                  <AddEdgeForm
                    node={node}
                    graph={graph}
                    disabled={pending}
                    onSubmit={(edge) =>
                      void run(
                        () =>
                          api.addTopologyEdge(taskId, {
                            src_ip: edge.src_ip,
                            dst_ip: node.ip,
                            kind: edge.kind,
                            label: edge.label,
                          }),
                        "联系已添加",
                      )
                    }
                  />
                </section>

                <section>
                  <h3 className="mb-1.5 text-sm font-semibold">人工备注</h3>
                  <Textarea
                    value={note}
                    maxLength={4000}
                    onChange={(e) => setNote(e.target.value)}
                    placeholder="记录人工判断，不改变原始证据。"
                    className="min-h-20"
                  />
                  <Button
                    className="mt-2"
                    variant="outline"
                    size="sm"
                    disabled={pending || !note.trim()}
                    onClick={() =>
                      void run(async () => {
                        const next = await api.addTopologyNote(taskId, node.ip, note.trim());
                        setNote("");
                        return next;
                      }, "备注已保存")
                    }
                  >
                    <Save className="size-3.5" />
                    保存备注
                  </Button>
                  <div className="mt-3 flex flex-col gap-2">
                    {notes.length === 0 && <p className="text-muted-foreground text-xs">暂无人工备注。</p>}
                    {notes.map((item) => (
                      <div key={item.id} className="rounded-md border p-2.5 text-xs">
                        <p className="whitespace-pre-wrap">{item.body}</p>
                        <p className="text-muted-foreground mt-1.5">
                          {item.created_by || "未知来源"} · {formatTime(item.created_at)}
                        </p>
                      </div>
                    ))}
                  </div>
                </section>

                <section className="border-t pt-3">
                  <AlertDialog>
                    <AlertDialogTrigger asChild>
                      <Button variant="outline" size="sm" disabled={pending}>
                        <Trash2 className="size-3.5" />
                        删除节点
                      </Button>
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>删除节点 {node.ip}？</AlertDialogTitle>
                        <AlertDialogDescription>
                          与之相连的联系会一并删除，变成孤儿的节点会由后端降级为待确认入口。
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>取消</AlertDialogCancel>
                        <AlertDialogAction
                          onClick={() =>
                            void run(async () => {
                              const next = await api.deleteTopologyNode(taskId, node.ip);
                              onDeleted();
                              return next;
                            }, "节点已删除")
                          }
                        >
                          删除
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
