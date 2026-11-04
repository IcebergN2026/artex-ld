/**
 * topology-import-dialog.tsx：拓扑图导入弹窗，解析导出文件并按后端接口幂等重建。
 * 主要函数/类型：
 *   - TopologyImportDialog：导入弹窗 UI，文件解析、逐条重建与进度日志
 *   - rebuildFromGraph：按 分组→节点→边→备注 顺序重建，已存在则复用/跳过
 */
"use client";

import * as React from "react";

import { FileUp, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { TopologyGraph, TopologyGraphEdge } from "@/lib/types";

import { normalizeImport } from "./graph-model";

function errorText(reason: unknown): string {
  if (reason instanceof Error) return reason.message;
  return String(reason);
}

/**
 * 把导出文件里的 topology 数据集按后端接口逐条重建。
 * 幂等策略：分组按名字复用，节点/IP 已存在则 PATCH 否则 POST，边已存在则跳过，备注按内容去重。
 */
async function rebuildFromGraph(
  taskId: string,
  file: TopologyGraph,
  current: TopologyGraph,
  log: (line: string) => void,
): Promise<TopologyGraph> {
  let graph = current;

  // 1) 分组先建，节点才能挂进框里。
  const groupIDByName = new Map(graph.groups.map((g) => [g.name, g.id]));
  for (const group of file.groups) {
    const body = {
      name: group.name,
      x: Math.round(group.x),
      y: Math.round(group.y),
      width: Math.round(group.width),
      height: Math.round(group.height),
      color: group.color,
    };
    const existing = groupIDByName.get(group.name);
    graph = existing ? await api.patchTopologyGroup(taskId, existing, body) : await api.addTopologyGroup(taskId, body);
    const created = graph.groups.find((g) => g.name === group.name);
    if (created) groupIDByName.set(group.name, created.id);
    log(`${existing ? "更新" : "新建"}分组 ${group.name}`);
  }

  // 每条节点的第一条入边随节点一起创建（后端要求节点存在才能连边）。
  const firstEdgeByDst = new Map<string, TopologyGraphEdge>();
  for (const edge of file.edges) {
    if (!firstEdgeByDst.has(edge.dst_ip)) firstEdgeByDst.set(edge.dst_ip, edge);
  }

  const fileGroupName = new Map(file.groups.map((g) => [g.id, g.name]));
  const consumedFirstEdges = new Set<TopologyGraphEdge>();
  const existingIPs = new Set(graph.nodes.map((n) => n.ip));

  // 2) 节点。
  for (const node of file.nodes) {
    const groupName = node.group_id == null ? null : fileGroupName.get(node.group_id);
    const groupID = groupName ? (groupIDByName.get(groupName) ?? null) : null;
    const patch = {
      hostname: node.hostname,
      display_label: node.display_label,
      group_id: groupID,
      x: Math.round(node.x),
      y: Math.round(node.y),
      entry_status: node.entry_status,
    };
    if (existingIPs.has(node.ip)) {
      graph = await api.patchTopologyNode(taskId, node.ip, patch);
      log(`更新节点 ${node.ip}`);
      continue;
    }
    const firstEdge = firstEdgeByDst.get(node.ip);
    graph = await api.addTopologyNode(
      taskId,
      { ip: node.ip, ...patch },
      firstEdge
        ? {
            src_ip: firstEdge.src_ip,
            dst_ip: node.ip,
            kind: firstEdge.kind,
            label: firstEdge.label,
            evidence_ref: firstEdge.evidence_ref,
          }
        : null,
    );
    const consumed = firstEdgeByDst.get(node.ip);
    if (consumed) consumedFirstEdges.add(consumed);
    log(`新建节点 ${node.ip}`);
  }

  // 3) 其余联系：已存在（按 源→目标→类型 判重）或重复的都跳过，成环等非法连边只记日志。
  const edgeKey = (src: string, dst: string, kind: string) => `${src}->${dst}:${kind}`;
  const known = new Set(graph.edges.map((e) => edgeKey(e.src_ip, e.dst_ip, e.kind)));
  for (const edge of file.edges) {
    if (consumedFirstEdges.has(edge)) continue;
    if (known.has(edgeKey(edge.src_ip, edge.dst_ip, edge.kind))) continue;
    try {
      graph = await api.addTopologyEdge(taskId, {
        src_ip: edge.src_ip,
        dst_ip: edge.dst_ip,
        kind: edge.kind,
        label: edge.label,
        evidence_ref: edge.evidence_ref,
      });
      known.add(edgeKey(edge.src_ip, edge.dst_ip, edge.kind));
    } catch (reason) {
      log(`跳过联系 ${edge.src_ip} → ${edge.dst_ip}：${errorText(reason)}`);
    }
  }

  // 4) 备注：同一 IP 下内容已存在则不重复写入。
  for (const [ip, notes] of Object.entries(file.notes)) {
    if (!graph.nodes.some((n) => n.ip === ip)) continue;
    const existingBodies = new Set((graph.notes[ip] ?? []).map((n) => n.body));
    for (const note of notes) {
      if (!note.body.trim() || existingBodies.has(note.body)) continue;
      graph = await api.addTopologyNote(taskId, ip, note.body);
      existingBodies.add(note.body);
      log(`新增备注 ${ip}`);
    }
  }

  return graph;
}

export function TopologyImportDialog({
  taskId,
  graph,
  onGraph,
  open,
  onOpenChange,
}: {
  taskId: string;
  graph: TopologyGraph;
  onGraph: (graph: TopologyGraph) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [text, setText] = React.useState("");
  const [fileName, setFileName] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [log, setLog] = React.useState<string[]>([]);

  async function handleFile(file: File | null) {
    if (!file) return;
    setFileName(file.name);
    setLog([]);
    try {
      setText(await file.text());
    } catch (reason) {
      toast.error(`读取文件失败：${errorText(reason)}`);
    }
  }

  async function handleImport() {
    let parsed: TopologyGraph;
    try {
      parsed = normalizeImport(text);
    } catch (reason) {
      toast.error(`文件不合法：${errorText(reason)}`);
      return;
    }
    setBusy(true);
    setLog([]);
    try {
      const next = await rebuildFromGraph(taskId, parsed, graph, (line) => setLog((prev) => [...prev, line]));
      onGraph(next);
      toast.success(`导入完成：${parsed.nodes.length} 个节点、${parsed.edges.length} 条联系`);
      onOpenChange(false);
    } catch (reason) {
      toast.error(`导入失败：${errorText(reason)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>导入 JSON</DialogTitle>
          <DialogDescription>
            读取 artex-asset-graph 文件的 topology 数据集，逐条调用接口重建。已存在的分组按名字复用、节点按 IP
            更新、联系与备注去重，不会清空现有图。
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <label className="flex cursor-pointer items-center gap-2 rounded-md border border-dashed p-3 text-sm">
            <FileUp className="text-muted-foreground size-4" />
            <span className="flex-1 truncate">{fileName || "选择 JSON 文件"}</span>
            <input
              type="file"
              accept="application/json,.json"
              className="hidden"
              onChange={(e) => void handleFile(e.target.files?.[0] ?? null)}
            />
          </label>
          {log.length > 0 && (
            <pre className="bg-muted/50 max-h-40 overflow-auto rounded-md border p-2 text-[11px] leading-relaxed">
              {log.join("\n")}
            </pre>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button onClick={() => void handleImport()} disabled={busy || !text}>
            {busy && <Loader2 className="size-3.5 animate-spin" />}
            开始导入
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
