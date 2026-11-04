/**
 * topology-editor.tsx：拓扑编辑器状态持有者，编排画布、节点抽屉、工具栏与各类弹窗。
 * 主要函数/类型：
 *   - TopologyEditor：持有整图状态，所有写操作以后端返回整图替换本地状态
 *   - groupBody / errorText：分组更新请求体构造与错误文案辅助
 */
"use client";

import * as React from "react";

import { Loader2, RefreshCw } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { TopologyGraph, TopologyGraphGroup, TopologyGraphNode } from "@/lib/types";

import { type BoxSelectRect, type NodeMovePayload, TopologyCanvas } from "./topology-canvas";
import { applyC24Grouping, planC24Groups } from "./c24-groups";
import { type ConnPair, CreateEdgeDialog } from "./topology-edge-dialog";
import { autoLayout } from "./graph-model";
import { TopologyNodeDrawer } from "./topology-node-drawer";
import { AddGroupDialog, type BoxGroupPrefill, type TopologyRun, TopologyToolbar } from "./topology-toolbar";

function errorText(reason: unknown): string {
  if (reason instanceof Error) return reason.message;
  return String(reason);
}

function groupBody(
  group: TopologyGraphGroup,
  overrides: Partial<{ name: string; x: number; y: number; width: number; height: number }> = {},
) {
  return {
    name: overrides.name ?? group.name,
    x: overrides.x ?? group.x,
    y: overrides.y ?? group.y,
    width: overrides.width ?? group.width,
    height: overrides.height ?? group.height,
    color: group.color,
  };
}

/**
 * 拓扑编辑器状态持有者。
 * 所有写操作都用后端返回的整图替换本地状态，前端不做入口/孤儿关系的推断。
 */
export function TopologyEditor({ taskId }: { taskId: string }) {
  const [graph, setGraph] = React.useState<TopologyGraph | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [loadError, setLoadError] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [selectedIP, setSelectedIP] = React.useState<string | null>(null);
  const [connectMode, setConnectMode] = React.useState(false);
  const [connectFrom, setConnectFrom] = React.useState<string | null>(null);
  const [connPair, setConnPair] = React.useState<ConnPair | null>(null);
  const [renamingGroup, setRenamingGroup] = React.useState<TopologyGraphGroup | null>(null);
  const [boxSelectMode, setBoxSelectMode] = React.useState(false);
  const [boxGroup, setBoxGroup] = React.useState<BoxGroupPrefill | null>(null);

  // 首次加载自动展开/归组：fact 自动建边写入的节点没有坐标（DB 默认 0,0），
  // 全部堆在原点；同 /24 的散点也应按网段归组。触发条件=「无分组」且（坐标
  // 全堆原点 或 存在成员≥2 的 /24 子网）；同名组复用不重建，已有任何分组不干预。
  // 顺序：先 applyC24Grouping 归组，再 autoLayout 两层布局回写（组内相对/组外绝对）。
  const maybeAutoLayout = React.useCallback(
    async (g: TopologyGraph) => {
      if (g.groups.length > 0) return;
      const ungrouped = g.nodes.filter((n) => n.group_id == null);
      const stacked = ungrouped.length >= 2 && ungrouped.every((n) => n.x === 0 && n.y === 0);
      if (!stacked && planC24Groups(g).length === 0) return;
      try {
        let current = await applyC24Grouping(taskId, g);
        const groupCount = current.groups.length;
        const plan = autoLayout(current);
        for (const [id, box] of plan.groupBoxes) {
          const group = current.groups.find((gg) => gg.id === id);
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
        setGraph(current);
        toast.success(`已自动展开 ${plan.positions.size} 个节点、${groupCount} 个网段分组`);
      } catch (reason) {
        toast.error(`自动展开失败：${errorText(reason)}`);
      }
    },
    [taskId],
  );

  const load = React.useCallback(
    async (silent = false) => {
      if (!silent) setLoading(true);
      try {
        const next = await api.taskTopology(taskId);
        setGraph(next);
        setLoadError(null);
        if (!silent) void maybeAutoLayout(next);
      } catch (reason) {
        setLoadError(errorText(reason));
        if (silent) toast.error(`刷新失败：${errorText(reason)}`);
      } finally {
        setLoading(false);
      }
    },
    [taskId, maybeAutoLayout],
  );

  React.useEffect(() => {
    setGraph(null);
    setSelectedIP(null);
    setConnectMode(false);
    setConnectFrom(null);
    setConnPair(null);
    setBoxSelectMode(false);
    setBoxGroup(null);
    void load();
  }, [load]);

  const run: TopologyRun = React.useCallback(async (task, success) => {
    setBusy(true);
    try {
      const next = await task();
      setGraph(next);
      if (success) toast.success(success);
      return next;
    } catch (reason) {
      toast.error(`操作失败：${errorText(reason)}`);
      return null;
    } finally {
      setBusy(false);
    }
  }, []);

  React.useEffect(() => {
    if (!connectMode) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      if (connectFrom) setConnectFrom(null);
      else setConnectMode(false);
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [connectMode, connectFrom]);

  // 框选建组：Esc 退出，选中节点后由对话框接管。
  React.useEffect(() => {
    if (!boxSelectMode) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setBoxSelectMode(false);
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [boxSelectMode]);

  const handleNodeClick = React.useCallback(
    (ip: string) => {
      if (!connectMode) {
        setSelectedIP(ip);
        return;
      }
      if (!connectFrom) {
        setConnectFrom(ip);
        return;
      }
      if (connectFrom === ip) {
        setConnectFrom(null);
        return;
      }
      setConnPair({ src: connectFrom, dst: ip });
      setConnectFrom(null);
      setConnectMode(false);
    },
    [connectMode, connectFrom],
  );

  const handleNodeMoved = React.useCallback(
    (payload: NodeMovePayload) => {
      if (!graph) return;
      const node = graph.nodes.find((n) => n.ip === payload.ip);
      if (!node) return;
      const sameGroup = (node.group_id ?? null) === payload.groupID;
      if (sameGroup && Math.round(node.x) === payload.x && Math.round(node.y) === payload.y) return;
      void run(() =>
        api.patchTopologyNode(taskId, payload.ip, {
          x: payload.x,
          y: payload.y,
          ...(sameGroup ? {} : { group_id: payload.groupID }),
        }),
      );
    },
    [graph, run, taskId],
  );

  const handleGroupMoved = React.useCallback(
    (group: TopologyGraphGroup, position: { x: number; y: number }) => {
      if (group.x === position.x && group.y === position.y) return;
      void run(() => api.patchTopologyGroup(taskId, group.id, groupBody(group, position)), "分组位置已保存");
    },
    [run, taskId],
  );

  const handleGroupResized = React.useCallback(
    (group: TopologyGraphGroup, params: { x: number; y: number; width: number; height: number }) => {
      const next = {
        x: Math.round(params.x),
        y: Math.round(params.y),
        width: Math.round(params.width),
        height: Math.round(params.height),
      };
      if (group.x === next.x && group.y === next.y && group.width === next.width && group.height === next.height)
        return;
      void run(() => api.patchTopologyGroup(taskId, group.id, groupBody(group, next)), "分组尺寸已保存");
    },
    [run, taskId],
  );

  // 选中分组后 NodeToolbar 的「删除分组」：工具栏内已做两步确认，这里直接执行。
  const handleGroupDelete = React.useCallback(
    (group: TopologyGraphGroup) => {
      void run(() => api.deleteTopologyGroup(taskId, group.id), "分组已删除，成员保留在画布上");
    },
    [run, taskId],
  );

  const handleBoxSelect = React.useCallback((rect: BoxSelectRect, members: TopologyGraphNode[]) => {
    if (members.length === 0) {
      toast.info("框选范围内没有节点");
      return;
    }
    // 进入分组创建流程即退出框选模式，由对话框接管后续交互。
    setBoxSelectMode(false);
    setBoxGroup({ rect, members });
  }, []);

  if (loading) {
    return (
      <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 text-sm">
        <Loader2 className="size-4 animate-spin" />
        加载拓扑…
      </div>
    );
  }

  if (!graph) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 text-sm">
        <p className="text-destructive">拓扑加载失败：{loadError ?? "未知错误"}</p>
        <Button size="sm" variant="outline" onClick={() => void load()}>
          <RefreshCw className="size-3.5" />
          重试
        </Button>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <TopologyToolbar
        taskId={taskId}
        graph={graph}
        run={run}
        onGraph={setGraph}
        connectMode={connectMode}
        onToggleConnect={() => {
          setConnectFrom(null);
          setConnectMode((prev) => !prev);
        }}
        boxSelectMode={boxSelectMode}
        onToggleBoxSelect={() => setBoxSelectMode((prev) => !prev)}
        renamingGroup={renamingGroup}
        onCloseRename={() => setRenamingGroup(null)}
      />
      <TopologyCanvas
        graph={graph}
        taskId={taskId}
        selectedIP={selectedIP}
        connectMode={connectMode}
        connectFrom={connectFrom}
        busy={busy}
        boxSelectMode={boxSelectMode}
        onBoxSelect={handleBoxSelect}
        onNodeClick={handleNodeClick}
        onGroupRename={setRenamingGroup}
        onGroupDelete={handleGroupDelete}
        onGroupMoved={handleGroupMoved}
        onGroupResized={handleGroupResized}
        onNodeMoved={handleNodeMoved}
        onPaneClick={() => {
          setConnectFrom(null);
          setConnectMode(false);
          setSelectedIP(null);
        }}
      />
      <TopologyNodeDrawer
        taskId={taskId}
        graph={graph}
        ip={selectedIP}
        onGraph={setGraph}
        onClose={() => setSelectedIP(null)}
        onDeleted={() => setSelectedIP(null)}
      />
      <AddGroupDialog
        taskId={taskId}
        graph={graph}
        run={run}
        open={boxGroup !== null}
        onOpenChange={(open) => {
          if (!open) setBoxGroup(null);
        }}
        prefill={boxGroup}
      />
      <CreateEdgeDialog taskId={taskId} pair={connPair} run={run} onClose={() => setConnPair(null)} />
    </div>
  );
}
