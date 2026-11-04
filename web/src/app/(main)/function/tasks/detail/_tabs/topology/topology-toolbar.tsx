/**
 * topology-toolbar.tsx：拓扑工具栏（自动布局、建组、导入导出、连线/框选开关）与分组弹窗。
 * 主要函数/类型：
 *   - TopologyToolbar：工具栏主组件；TopologyRun：写操作统一执行器（跑请求+toast+刷新整图）
 *   - AddGroupDialog / RenameGroupDialog：新建（支持框选预填）与重命名分组弹窗
 *   - BoxGroupPrefill：框选建组的预填几何与成员
 */
"use client";

import * as React from "react";

import { BoxSelect, Download, Group, LayoutGrid, Link2, Loader2, Sparkles, Upload } from "lucide-react";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { api } from "@/lib/api";
import type { TopologyGraph, TopologyGraphGroup, TopologyGraphNode } from "@/lib/types";

import { exportDrawIO, exportGraphML } from "./export-formats";
import {
  applyC24Grouping,
} from "./c24-groups";
import {
  autoLayout,
  buildExportPayload,
  downloadJSON,
  GROUP_DEFAULT_H,
  GROUP_DEFAULT_W,
  nextGroupPlacement,
} from "./graph-model";
import { TopologyImportDialog } from "./topology-import-dialog";

export type TopologyRun = (task: () => Promise<TopologyGraph>, success?: string) => Promise<TopologyGraph | null>;

/** 框选建组的预填几何与成员（来自画布的 onBoxSelect）。 */
export type BoxGroupPrefill = {
  rect: { x: number; y: number; width: number; height: number };
  members: TopologyGraphNode[];
};

export function AddGroupDialog({
  taskId,
  graph,
  run,
  open,
  onOpenChange,
  prefill = null,
}: {
  taskId: string;
  graph: TopologyGraph;
  run: TopologyRun;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  prefill?: BoxGroupPrefill | null;
}) {
  const [name, setName] = React.useState("");

  // 打开时清空名称草稿（框选预填时名称为空，由用户命名）。
  React.useEffect(() => {
    if (open) setName("");
  }, [open]);

  async function handleSubmit() {
    const groupName = name.trim();
    if (!groupName) {
      toast.error("请填写分组名称");
      return;
    }
    // 框选预填：分组几何取框选矩形（做最小尺寸兜底）；否则沿用右侧自动摆放。
    const geometry = prefill
      ? {
          x: Math.round(prefill.rect.x),
          y: Math.round(prefill.rect.y),
          width: Math.max(Math.round(prefill.rect.width), 160),
          height: Math.max(Math.round(prefill.rect.height), 100),
        }
      : { ...nextGroupPlacement(graph), width: GROUP_DEFAULT_W, height: GROUP_DEFAULT_H };
    const next = await run(async () => {
      let current = await api.addTopologyGroup(taskId, { name: groupName, ...geometry });
      if (prefill && prefill.members.length > 0) {
        // 后端只回整图：用名称 + 几何定位刚创建的分组，再逐个把成员挪进去。
        const created = current.groups.find(
          (group) =>
            group.name === groupName &&
            group.x === geometry.x &&
            group.y === geometry.y &&
            group.width === geometry.width &&
            group.height === geometry.height,
        );
        if (created) {
          for (const member of prefill.members) {
            current = await api.patchTopologyNode(taskId, member.ip, { group_id: created.id });
          }
        }
      }
      return current;
    }, "分组已添加");
    if (next) onOpenChange(false);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>添加分组</DialogTitle>
          <DialogDescription>
            {prefill
              ? `将把框选到的 ${prefill.members.length} 个节点归入该分组。`
              : "拖动节点入框即可归组，框本身也可整体拖动。"}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="topology-group-name">分组名称</Label>
          <Input
            id="topology-group-name"
            value={name}
            maxLength={120}
            placeholder="例如：运维网段"
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void handleSubmit();
            }}
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void handleSubmit()}>创建</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RenameGroupDialog({
  taskId,
  group,
  run,
  onClose,
}: {
  taskId: string;
  group: TopologyGraphGroup | null;
  run: TopologyRun;
  onClose: () => void;
}) {
  const [name, setName] = React.useState("");

  React.useEffect(() => {
    setName(group?.name ?? "");
  }, [group]);

  async function handleSubmit() {
    if (!group) return;
    const next = await run(
      () =>
        api.patchTopologyGroup(taskId, group.id, {
          name: name.trim() || group.name,
          x: group.x,
          y: group.y,
          width: group.width,
          height: group.height,
          color: group.color,
        }),
      "分组已更新",
    );
    if (next) onClose();
  }

  const [confirmingDelete, setConfirmingDelete] = React.useState(false);
  // 分组切换时在渲染期重置确认态（React 推荐的 derive-state 模式，避免 effect 依赖告警）。
  const [confirmingFor, setConfirmingFor] = React.useState<number | null>(null);
  if ((group?.id ?? null) !== confirmingFor) {
    setConfirmingFor(group?.id ?? null);
    setConfirmingDelete(false);
  }

  async function handleDelete() {
    if (!group) return;
    const next = await run(() => api.deleteTopologyGroup(taskId, group.id), "分组已删除，成员保留在画布上");
    if (next) onClose();
  }

  return (
    <Dialog
      open={group !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>重命名分组</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="topology-group-rename">分组名称</Label>
          <Input
            id="topology-group-rename"
            value={name}
            maxLength={120}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void handleSubmit();
            }}
          />
        </div>
        <DialogFooter className="gap-2 sm:justify-between">
          {confirmingDelete ? (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => void handleDelete()}
              title="成员节点会保留在画布上（退出分组）"
            >
              确认删除？成员保留
            </Button>
          ) : (
            <Button variant="ghost" size="sm" className="text-destructive" onClick={() => setConfirmingDelete(true)}>
              删除分组
            </Button>
          )}
          <div className="flex gap-2">
            <Button variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button onClick={() => void handleSubmit()}>保存</Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function TopologyToolbar({
  taskId,
  graph,
  run,
  onGraph,
  connectMode,
  onToggleConnect,
  boxSelectMode,
  onToggleBoxSelect,
  renamingGroup,
  onCloseRename,
}: {
  taskId: string;
  graph: TopologyGraph;
  run: TopologyRun;
  onGraph: (graph: TopologyGraph) => void;
  connectMode: boolean;
  onToggleConnect: () => void;
  boxSelectMode: boolean;
  onToggleBoxSelect: () => void;
  renamingGroup: TopologyGraphGroup | null;
  onCloseRename: () => void;
}) {
  const [addGroupOpen, setAddGroupOpen] = React.useState(false);
  const [importOpen, setImportOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [exporting, setExporting] = React.useState(false);

  // 全量两层布局：先按 /24 归组散点（同名组复用、已有组不动），再先写分组框
  // （位置+收拢后尺寸），后写节点（组内相对/组外绝对）。
  async function handleAutoLayout() {
    const working = await applyC24Grouping(taskId, graph);
    const plan = autoLayout(working);
    if (plan.positions.size === 0 && plan.groupBoxes.size === 0) {
      toast.info("没有可自动整理的节点");
      return;
    }
    setBusy(true);
    try {
      const next = await run(async () => {
        let current = working;
        for (const [id, box] of plan.groupBoxes) {
          const group = current.groups.find((g) => g.id === id);
          if (!group) continue;
          current = await api.patchTopologyGroup(taskId, id, {
            name: group.name,
            x: box.x,
            y: box.y,
            width: box.width,
            height: box.height,
            color: group.color,
          });
        }
        for (const [ip, pos] of plan.positions) {
          current = await api.patchTopologyNode(taskId, ip, { x: pos.x, y: pos.y });
        }
        return current;
      });
      if (next) toast.success(`已重排 ${plan.positions.size} 个节点、${plan.groupBoxes.size} 个分组框`);
    } finally {
      setBusy(false);
    }
  }

  async function handleExportJSON() {
    setExporting(true);
    try {
      const coverage = await api.taskCoverageGraph(taskId).catch(() => null);
      const payload = buildExportPayload(taskId, graph, coverage);
      downloadJSON(`artex-asset-graph-${taskId}.json`, payload);
      toast.success("已导出 JSON");
    } finally {
      setExporting(false);
    }
  }

  function downloadText(filename: string, text: string, mime: string) {
    const blob = new Blob([text], { type: mime });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    link.click();
    URL.revokeObjectURL(url);
  }

  function handleExportGraphML() {
    downloadText(`artex-topology-${taskId}.graphml`, exportGraphML(graph), "application/graphml+xml");
    toast.success("已导出 GraphML（可用 yEd/Gephi/draw.io 打开编辑）");
  }

  function handleExportDrawIO() {
    downloadText(`artex-topology-${taskId}.drawio`, exportDrawIO(graph), "application/xml");
    toast.success("已导出 draw.io 文件（可在 diagrams.net 打开编辑）");
  }

  const emptyGraph = graph.nodes.length === 0;

  async function handleSeed() {
    const next = await run(() => api.seedTopology(taskId), "已从任务资产生成初始拓扑");
    if (next) onGraph(next);
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        size="sm"
        variant={connectMode ? "default" : "outline"}
        onClick={onToggleConnect}
        aria-pressed={connectMode}
      >
        <Link2 className="size-3.5" />
        添加联系
      </Button>
      <Button size="sm" variant="outline" onClick={() => setAddGroupOpen(true)}>
        <Group className="size-3.5" />
        添加分组
      </Button>
      <Button
        size="sm"
        variant={boxSelectMode ? "default" : "outline"}
        onClick={onToggleBoxSelect}
        aria-pressed={boxSelectMode}
        title="在画布空白处拖出框选区域，把区域内节点建成分组"
      >
        <BoxSelect className="size-3.5" />
        框选建组
      </Button>
      <Separator orientation="vertical" className="mx-1 h-6" />
      <Button size="sm" variant="outline" onClick={() => void handleAutoLayout()} disabled={busy}>
        {busy ? <Loader2 className="size-3.5 animate-spin" /> : <LayoutGrid className="size-3.5" />}
        自动整理
      </Button>
      <Separator orientation="vertical" className="mx-1 h-6" />
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="outline" disabled={exporting}>
            <Download className="size-3.5" />
            导出
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem onSelect={() => void handleExportJSON()}>JSON（artex 格式，可再导入）</DropdownMenuItem>
          <DropdownMenuItem onSelect={handleExportGraphML}>GraphML（yEd / Gephi / draw.io）</DropdownMenuItem>
          <DropdownMenuItem onSelect={handleExportDrawIO}>draw.io（diagrams.net）</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button size="sm" variant="outline" onClick={() => setImportOpen(true)}>
        <Upload className="size-3.5" />
        导入 JSON
      </Button>
      {emptyGraph && (
        <>
          <Separator orientation="vertical" className="mx-1 h-6" />
          <Button size="sm" onClick={() => void handleSeed()} disabled={busy}>
            <Sparkles className="size-3.5" />
            从资产生成初始拓扑
          </Button>
        </>
      )}

      <AddGroupDialog taskId={taskId} graph={graph} run={run} open={addGroupOpen} onOpenChange={setAddGroupOpen} />
      <RenameGroupDialog taskId={taskId} group={renamingGroup} run={run} onClose={onCloseRename} />
      <TopologyImportDialog
        taskId={taskId}
        graph={graph}
        onGraph={onGraph}
        open={importOpen}
        onOpenChange={setImportOpen}
      />
    </div>
  );
}
