/**
 * topology-canvas.tsx：拓扑图画布，基于 React Flow 渲染自定义节点卡片/分组框/连线并处理交互。
 * 主要函数/类型：
 *   - TopologyCanvas / TopologyCanvasInner：画布组件，整图转 RF 节点/边，处理点选、移动、框选回调
 *   - TopologyCanvasProps：画布入参契约；NodeMovePayload / BoxSelectRect：移动与框选载荷类型
 */
"use client";
"use no memo";

import * as React from "react";

import {
  Background,
  BackgroundVariant,
  BaseEdge,
  Controls,
  EdgeLabelRenderer,
  type EdgeProps,
  getStraightPath,
  Handle,
  MarkerType,
  MiniMap,
  type NodeProps,
  NodeResizer,
  NodeToolbar,
  Position,
  ReactFlow,
  ReactFlowProvider,
  type Edge as RFEdge,
  type Node as RFNode,
  useEdgesState,
  useNodesState,
  useReactFlow,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Crosshair, Link2, Loader2 } from "lucide-react";

import type { TopologyGraph, TopologyGraphGroup, TopologyGraphNode } from "@/lib/types";
import { cn } from "@/lib/utils";

import {
  absolutePosition,
  EDGE_KIND_META,
  ENTRY_STATUS_LABEL,
  GROUP_DEFAULT_H,
  GROUP_DEFAULT_W,
  groupSize,
  NODE_H,
  NODE_W,
  nodeSubtitle,
  nodeTitle,
} from "./graph-model";

const GROUP_PREFIX = "group-";

function groupNodeID(id: number): string {
  return `${GROUP_PREFIX}${id}`;
}

/** 入口状态 → 卡片描边与角标（描边在 dark 下同样可读，Tailwind 只负责角标配色）。 */
type EntryTone = {
  stroke: string;
  dashed: boolean;
  badge: string;
  badgeClass: string;
};

function entryTone(status: string): EntryTone {
  if (status === "confirmed") {
    return {
      stroke: "#10b981",
      dashed: false,
      badge: "入口",
      badgeClass:
        "border-emerald-300 bg-emerald-50 text-emerald-700 dark:border-emerald-700 dark:bg-emerald-950/70 dark:text-emerald-300",
    };
  }
  if (status === "suspected") {
    return {
      stroke: "#f59e0b",
      dashed: true,
      badge: "疑似入口",
      badgeClass:
        "border-amber-300 bg-amber-50 text-amber-700 dark:border-amber-700 dark:bg-amber-950/70 dark:text-amber-300",
    };
  }
  return { stroke: "", dashed: false, badge: "", badgeClass: "" };
}

// ---------------------------------------------------------------------------
// 自定义元素：draw.io 风格的节点卡片 / 分组容器 / smoothstep 连线。
// ---------------------------------------------------------------------------

type TopoCardData = {
  title: string;
  subtitle: string;
  /** 节点事实一句话（"是什么"），空串不渲染第三行。 */
  description: string;
  badge: string;
  badgeClass: string;
  toneStroke: string;
  toneDashed: boolean;
  connectFrom: boolean;
  /** 节点自定义颜色（"" = 默认）。 */
  color: string;
};
type TopoCardNode = RFNode<TopoCardData, "topology">;

type GroupCardData = {
  name: string;
  width: number;
  height: number;
  /** 缩放结束（含位置）回传编辑器持久化。 */
  onResized: (params: { x: number; y: number; width: number; height: number }) => void;
  onRename: () => void;
  onDelete: () => void;
};
/**
 * 类型名刻意不用 "group"：RF 基础样式给 .react-flow__node-group 写死
 * width:150px + padding:10px + 边框/底色，会把自定义框压成 150px（实测的"外框 bug"）。
 */
type GroupCardNode = RFNode<GroupCardData, "frame">;

type CanvasNode = TopoCardNode | GroupCardNode;

function TopoCard({ data, selected }: NodeProps<TopoCardNode>) {
  // 自定义颜色优先：非空时描边与标题都用它（draw.io 的样式覆盖语义）。
  const borderColor = data.color || data.toneStroke || undefined;
  return (
    <div
      className={cn(
        "flex h-full w-full flex-col justify-center gap-0.5 rounded-lg border bg-white px-3 py-2 shadow-none transition-shadow hover:shadow-md dark:bg-neutral-900",
        "border-[#666666] dark:border-neutral-600",
        data.toneDashed && "border-dashed",
        data.connectFrom && "border-2 border-[#4d90fe]",
        selected && "border-[#4d90fe] shadow-md ring-1 ring-[#4d90fe]/40",
      )}
      style={{ width: NODE_W, height: NODE_H, borderColor }}
    >
      <Handle type="target" position={Position.Left} className="!size-2 !opacity-0" />
      <div className="flex min-w-0 items-center gap-1.5">
        <span
          className="truncate font-semibold text-[13px] text-slate-900 dark:text-neutral-100"
          style={data.color ? { color: data.color } : undefined}
        >
          {data.title}
        </span>
        {data.badge ? (
          <span className={cn("ml-auto shrink-0 rounded-full border px-1.5 py-px text-[10px]", data.badgeClass)}>
            {data.badge}
          </span>
        ) : null}
      </div>
      <span className="truncate font-mono text-[11px] text-slate-500 dark:text-neutral-400">{data.subtitle}</span>
      {data.description ? (
        <span
          className="max-w-full truncate text-[10px] leading-tight text-slate-400 dark:text-neutral-500"
          title={data.description}
        >
          {data.description}
        </span>
      ) : null}
      <Handle type="source" position={Position.Right} className="!size-2 !opacity-0" />
    </div>
  );
}

function GroupCard({ data, selected, id }: NodeProps<GroupCardNode>) {
  const [confirmingDelete, setConfirmingDelete] = React.useState(false);
  // 选中变化（含删除后节点卸载前）时复位确认态。
  if (!selected && confirmingDelete) setConfirmingDelete(false);
  return (
    <div
      className={cn(
        "relative h-full w-full rounded-lg border border-dashed",
        selected ? "border-[#4d90fe]" : "border-[#666666] dark:border-neutral-600",
      )}
      style={{ width: data.width, height: data.height }}
    >
      {/* 分组名常驻显示（原先仅在选中时的 NodeToolbar 里可见）。 */}
      <div className="pointer-events-none absolute -top-6 left-1 max-w-full truncate text-xs font-medium text-muted-foreground">
        {data.name}
      </div>
      <NodeToolbar
        isVisible={Boolean(selected)}
        nodeId={id}
        position={Position.Top}
        offset={8}
        className="flex items-center gap-1 rounded-md border bg-card p-1 shadow-sm"
      >
        <span className="max-w-32 truncate px-1.5 text-muted-foreground text-xs">{data.name}</span>
        <button
          type="button"
          className="rounded px-2 py-1 text-xs hover:bg-accent"
          onClick={(event) => {
            event.stopPropagation();
            data.onRename();
          }}
        >
          重命名
        </button>
        {confirmingDelete ? (
          <button
            type="button"
            className="rounded bg-destructive px-2 py-1 text-destructive-foreground text-xs hover:bg-destructive/90"
            onClick={(event) => {
              event.stopPropagation();
              setConfirmingDelete(false);
              data.onDelete();
            }}
          >
            确认删除？成员保留
          </button>
        ) : (
          <button
            type="button"
            className="rounded px-2 py-1 text-destructive text-xs hover:bg-destructive/10"
            onClick={(event) => {
              event.stopPropagation();
              setConfirmingDelete(true);
            }}
          >
            删除分组
          </button>
        )}
      </NodeToolbar>
      <NodeResizer
        isVisible={Boolean(selected)}
        minWidth={220}
        minHeight={140}
        lineClassName="!border-[#4d90fe]"
        handleClassName="!size-2 !rounded-sm !border !border-[#4d90fe] !bg-white dark:!bg-neutral-900"
        onResizeEnd={(_event, params) => data.onResized(params)}
      />
    </div>
  );
}

type TopoEdgeData = { label: string };
type TopoRFEdge = RFEdge<TopoEdgeData>;

function TopologyEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  data,
  style,
  markerEnd,
}: EdgeProps<TopoRFEdge>) {
  const [path, labelX, labelY] = getStraightPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
  });
  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style} />
      <EdgeLabelRenderer>
        <span
          className="pointer-events-none absolute max-w-40 truncate rounded border border-slate-200 bg-white/90 px-1.5 py-px text-[10px] text-slate-600 dark:border-neutral-700 dark:bg-neutral-900/90 dark:text-neutral-300"
          style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
          title={data?.label}
        >
          {data?.label}
        </span>
      </EdgeLabelRenderer>
    </>
  );
}

const nodeTypes = { topology: TopoCard, frame: GroupCard };
const edgeTypes = { topology: TopologyEdge };

// ---------------------------------------------------------------------------
// 后端整图 → React Flow 数据。
// ---------------------------------------------------------------------------

/**
 * 图数据转 RF 节点。坐标以 DB 为准（组内节点是相对坐标，天然匹配 RF 的 parentId 语义）；
 * 仅当父子关系没变时保留画布上的当前位置，避免轮询/保存回包时节点跳回旧坐标。
 */
function buildRFNodes(
  graph: TopologyGraph,
  selectedIP: string | null,
  connectFrom: string | null,
  prev: CanvasNode[],
  onGroupResized: (group: TopologyGraphGroup, params: { x: number; y: number; width: number; height: number }) => void,
  onGroupAction: (group: TopologyGraphGroup, action: "rename" | "delete") => void,
): CanvasNode[] {
  const prevByID = new Map(prev.map((node) => [node.id, node]));
  const groupIDs = new Set(graph.groups.map((group) => group.id));

  const groupNodes: GroupCardNode[] = graph.groups.map((group) => {
    const id = groupNodeID(group.id);
    const size = groupSize(group);
    return {
      id,
      type: "frame" as const,
      position: prevByID.get(id)?.position ?? { x: group.x, y: group.y },
      data: {
        name: group.name,
        width: size.width,
        height: size.height,
        onResized: (params) => onGroupResized(group, params),
        onRename: () => onGroupAction(group, "rename"),
        onDelete: () => onGroupAction(group, "delete"),
      },
      zIndex: -1,
    };
  });

  const nodes: TopoCardNode[] = graph.nodes.map((node) => {
    const parentID = node.group_id != null && groupIDs.has(node.group_id) ? groupNodeID(node.group_id) : undefined;
    const prevNode = prevByID.get(node.ip);
    const tone = entryTone(node.entry_status);
    return {
      id: node.ip,
      type: "topology",
      parentId: parentID,
      position: prevNode && prevNode.parentId === parentID ? prevNode.position : { x: node.x, y: node.y },
      data: {
        title: nodeTitle(node),
        subtitle: nodeSubtitle(node),
        description: node.description ?? "",
        badge: tone.badge,
        badgeClass: tone.badgeClass,
        toneStroke: tone.stroke,
        toneDashed: tone.dashed,
        connectFrom: connectFrom === node.ip,
        color: node.color ?? "",
      },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
      selected: selectedIP === node.ip,
      zIndex: 0,
    };
  });

  return [...groupNodes, ...nodes];
}

function buildRFEdges(graph: TopologyGraph): TopoRFEdge[] {
  const ipSet = new Set(graph.nodes.map((node) => node.ip));
  return graph.edges
    .filter((edge) => ipSet.has(edge.src_ip) && ipSet.has(edge.dst_ip))
    .map((edge) => {
      const meta = EDGE_KIND_META[edge.kind] ?? EDGE_KIND_META.reachable;
      const stroke = edge.color || meta.stroke;
      return {
        id: `edge-${edge.id}`,
        type: "topology",
        source: edge.src_ip,
        target: edge.dst_ip,
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
        data: { label: edge.label?.trim() || meta.label },
        style: {
          stroke,
          strokeWidth: 1.5,
          ...(meta.dash ? { strokeDasharray: meta.dash } : {}),
        },
        markerEnd: { type: MarkerType.ArrowClosed, color: stroke, width: 12, height: 12 },
      };
    });
}

/** 把框选 rect 归一化为 {x,y 左上角 + 正宽高}（flow 坐标与屏幕坐标 y 轴同向，直接 min/max）。 */
function normalizeRect(
  a: { x: number; y: number },
  b: { x: number; y: number },
): {
  x: number;
  y: number;
  width: number;
  height: number;
} {
  return {
    x: Math.min(a.x, b.x),
    y: Math.min(a.y, b.y),
    width: Math.abs(a.x - b.x),
    height: Math.abs(a.y - b.y),
  };
}

export type NodeMovePayload = { ip: string; groupID: number | null; x: number; y: number };
export type BoxSelectRect = { x: number; y: number; width: number; height: number };

export type TopologyCanvasProps = {
  graph: TopologyGraph;
  taskId: string;
  selectedIP: string | null;
  connectMode: boolean;
  connectFrom: string | null;
  busy?: boolean;
  boxSelectMode?: boolean;
  onBoxSelect?: (rect: BoxSelectRect, members: TopologyGraphNode[]) => void;
  onNodeClick: (ip: string) => void;
  onGroupRename: (group: TopologyGraphGroup) => void;
  onGroupDelete: (group: TopologyGraphGroup) => void;
  onGroupMoved: (group: TopologyGraphGroup, position: { x: number; y: number }) => void;
  onGroupResized: (group: TopologyGraphGroup, params: { x: number; y: number; width: number; height: number }) => void;
  onNodeMoved: (payload: NodeMovePayload) => void;
  onPaneClick: () => void;
};

// taskId 仍在 Props 契约里（topology-editor 会传），画布内不再用到它。
export function TopologyCanvas(props: TopologyCanvasProps) {
  return (
    <ReactFlowProvider>
      <TopologyCanvasInner {...props} />
    </ReactFlowProvider>
  );
}

function TopologyCanvasInner({
  graph,
  selectedIP,
  connectMode,
  connectFrom,
  busy = false,
  boxSelectMode = false,
  onBoxSelect,
  onNodeClick,
  onGroupRename,
  onGroupDelete,
  onGroupMoved,
  onGroupResized,
  onNodeMoved,
  onPaneClick,
}: TopologyCanvasProps) {
  const { screenToFlowPosition } = useReactFlow();
  const [rfNodes, setRfNodes, onNodesChange] = useNodesState<CanvasNode>([]);
  const [rfEdges, setRfEdges, onEdgesChange] = useEdgesState<TopoRFEdge>([]);
  const wrapperRef = React.useRef<HTMLDivElement | null>(null);
  /** 拖拽中的橡皮筋（wrapper 局部坐标，仅用于渲染）。 */
  const [band, setBand] = React.useState<BoxSelectRect | null>(null);
  const bandRef = React.useRef<{ startLocal: { x: number; y: number }; startFlow: { x: number; y: number } } | null>(
    null,
  );

  // 回调与图都走 ref：框选的全局 mousemove/mouseup 闭包创建后也能读到最新值。
  const handlersRef = React.useRef({ graph, onBoxSelect });
  handlersRef.current = { graph, onBoxSelect };
  // 分组回调同理走 ref：buildRFNodes 在 effect 里调用，不能因回调身份变化而重跑。
  const groupHandlersRef = React.useRef({
    onResized: onGroupResized,
    onRename: onGroupRename,
    onDelete: onGroupDelete,
  });
  groupHandlersRef.current = { onResized: onGroupResized, onRename: onGroupRename, onDelete: onGroupDelete };

  // 后端整图 / 选中态 / 连线起点变化 → 重建节点与边（保留画布当前位置，不做本地推断）。
  React.useEffect(() => {
    setRfNodes((prev) =>
      buildRFNodes(
        graph,
        selectedIP,
        connectFrom,
        prev,
        (group, params) => groupHandlersRef.current.onResized(group, params),
        (group, action) =>
          action === "rename" ? groupHandlersRef.current.onRename(group) : groupHandlersRef.current.onDelete(group),
      ),
    );
  }, [graph, selectedIP, connectFrom, setRfNodes]);

  React.useEffect(() => {
    setRfEdges(buildRFEdges(graph));
  }, [graph, setRfEdges]);

  // 退出框选模式时中止进行中的框选（已挂的全局监听在 mouseup 里看到 bandRef 为空会直接返回）。
  React.useEffect(() => {
    if (!boxSelectMode) bandRef.current = null;
  }, [boxSelectMode]);

  const handleWrapperMouseDown = (event: React.MouseEvent<HTMLDivElement>) => {
    if (!boxSelectMode || event.button !== 0) return;
    // 只有空白画布的 mousedown 才起框；节点/控件上的不触发。
    if (!(event.target as HTMLElement).closest(".react-flow__pane")) return;
    const wrapper = wrapperRef.current;
    if (!wrapper) return;
    const bounds = wrapper.getBoundingClientRect();
    const toLocal = (clientX: number, clientY: number) => ({ x: clientX - bounds.left, y: clientY - bounds.top });
    const startLocal = toLocal(event.clientX, event.clientY);
    const startFlow = screenToFlowPosition({ x: event.clientX, y: event.clientY });
    bandRef.current = { startLocal, startFlow };

    const onMove = (moveEvent: MouseEvent) => {
      if (!bandRef.current) return;
      setBand(normalizeRect(bandRef.current.startLocal, toLocal(moveEvent.clientX, moveEvent.clientY)));
    };
    const onUp = (upEvent: MouseEvent) => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
      const pending = bandRef.current;
      bandRef.current = null;
      setBand(null);
      if (!pending) return;
      const endLocal = toLocal(upEvent.clientX, upEvent.clientY);
      // 小于阈值的拖拽视为普通点击，交给 React Flow 的 onPaneClick。
      if (Math.abs(endLocal.x - pending.startLocal.x) < 4 && Math.abs(endLocal.y - pending.startLocal.y) < 4) return;
      const { graph: current, onBoxSelect: emit } = handlersRef.current;
      if (!emit) return;
      const rect = normalizeRect(pending.startFlow, screenToFlowPosition({ x: upEvent.clientX, y: upEvent.clientY }));
      // 成员判定：绝对中心落在 rect 内（简单数学，无需 hook）。
      const members = current.nodes.filter((node) => {
        const abs = absolutePosition(node, current.groups);
        const cx = abs.x + NODE_W / 2;
        const cy = abs.y + NODE_H / 2;
        return cx >= rect.x && cx <= rect.x + rect.width && cy >= rect.y && cy <= rect.y + rect.height;
      });
      emit(rect, members);
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  };

  const handleNodeDragStop = (_event: MouseEvent | TouchEvent, node: RFNode) => {
    if (node.type === "frame") {
      const group = graph.groups.find((item) => groupNodeID(item.id) === node.id);
      if (group) {
        onGroupMoved(group, { x: Math.round(node.position.x), y: Math.round(node.position.y) });
      }
      return;
    }
    const topo = graph.nodes.find((item) => item.ip === node.id);
    if (!topo) return;
    // RF 里子节点坐标相对父节点左上角；DB 的组内坐标同语义，换算回绝对坐标做命中判定。
    const oldGroup = node.parentId ? graph.groups.find((item) => groupNodeID(item.id) === node.parentId) : undefined;
    const absX = node.position.x + (oldGroup ? oldGroup.x : 0);
    const absY = node.position.y + (oldGroup ? oldGroup.y : 0);
    const centerX = absX + NODE_W / 2;
    const centerY = absY + NODE_H / 2;
    const hit = graph.groups.find((group) => {
      const size = groupSize(group);
      return (
        centerX >= group.x && centerX <= group.x + size.width && centerY >= group.y && centerY <= group.y + size.height
      );
    });
    if (!hit) {
      onNodeMoved({ ip: node.id, groupID: null, x: Math.round(absX), y: Math.round(absY) });
      return;
    }
    if ((topo.group_id ?? null) === hit.id) {
      // 留在原组：直接换算回组内相对坐标，不做边界夹取（与原实现一致）。
      onNodeMoved({ ip: node.id, groupID: hit.id, x: Math.round(absX - hit.x), y: Math.round(absY - hit.y) });
      return;
    }
    const size = groupSize(hit);
    onNodeMoved({
      ip: node.id,
      groupID: hit.id,
      x: Math.round(Math.min(Math.max(absX - hit.x, 0), Math.max(size.width - NODE_W, 0))),
      y: Math.round(Math.max(absY - hit.y, 0)),
    });
  };

  const handleNodeDoubleClick = (_event: React.MouseEvent, node: RFNode) => {
    if (node.type !== "frame") return;
    const group = graph.groups.find((item) => groupNodeID(item.id) === node.id);
    if (group) onGroupRename(group);
  };

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: 画布容器需要捕获鼠标拖拽以绘制框选矩形（仅框选模式下响应）。
    <div
      ref={wrapperRef}
      role="presentation"
      className={cn("relative min-h-0 flex-1 overflow-hidden rounded-lg border", boxSelectMode && "cursor-crosshair")}
      onMouseDown={handleWrapperMouseDown}
    >
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={(_, node) => {
          if (node.type !== "frame") onNodeClick(node.id);
        }}
        onNodeDoubleClick={handleNodeDoubleClick}
        onNodeDragStop={handleNodeDragStop}
        onPaneClick={() => onPaneClick()}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.1}
        proOptions={{ hideAttribution: true }}
        nodesConnectable={false}
        nodesDraggable={!connectMode}
        panOnDrag={!boxSelectMode}
        deleteKeyCode={null}
        className="!bg-[#f0f2f7] dark:!bg-neutral-950"
      >
        <Background variant={BackgroundVariant.Lines} gap={10} className="text-neutral-300 dark:text-neutral-800" />
        <Controls
          showInteractive={false}
          className="!rounded-lg !border !shadow-sm [&>button]:!border-border [&>button]:!bg-card [&>button:hover]:!bg-accent [&_svg]:!fill-foreground"
        />
        <MiniMap
          pannable
          zoomable
          className="!rounded-lg !border !bg-card !shadow-sm"
          maskColor="rgb(148 163 184 / 0.18)"
          nodeColor={(node) => {
            if (node.type === "frame") return "rgba(148, 163, 184, 0.25)";
            const data = node.data as TopoCardData | undefined;
            return data?.color || data?.toneStroke || "#94a3b8";
          }}
          nodeStrokeWidth={0}
          nodeBorderRadius={4}
        />
      </ReactFlow>

      {band ? (
        <div
          className="pointer-events-none absolute z-10 border border-[#4d90fe] bg-[#4d90fe]/10"
          style={{ left: band.x, top: band.y, width: band.width, height: band.height }}
        />
      ) : null}

      {/* 图例（组合色 + 入口状态） */}
      <div className="pointer-events-none absolute top-3 left-3 flex flex-col gap-2 rounded-lg border bg-card/95 p-3 text-xs shadow-sm backdrop-blur">
        <div className="flex flex-wrap gap-x-3 gap-y-1.5 text-foreground">
          {Object.entries(EDGE_KIND_META).map(([kind, meta]) => (
            <span key={kind} className="inline-flex items-center gap-1.5" title={meta.hint}>
              <span
                className={cn("w-5 border-t-2", meta.dash ? "border-dashed" : "border-solid")}
                style={{ borderColor: meta.stroke }}
              />
              {meta.label}
            </span>
          ))}
        </div>
        <div className="flex flex-wrap gap-x-3 gap-y-1 border-border/60 border-t pt-2 text-muted-foreground">
          {Object.entries(ENTRY_STATUS_LABEL).map(([status, label]) => (
            <span key={status || "none"}>{label}</span>
          ))}
          <span>{`分组框 ${GROUP_DEFAULT_W}×${GROUP_DEFAULT_H} 可拖动`}</span>
        </div>
      </div>

      {connectMode ? (
        <div className="pointer-events-none absolute top-3 left-1/2 -translate-x-1/2">
          <div className="flex items-center gap-2 rounded-lg border border-blue-400/60 bg-blue-50 px-3 py-1.5 font-medium text-blue-700 text-xs shadow-sm dark:bg-blue-950/70 dark:text-blue-200">
            <Link2 className="size-3.5" />
            {connectFrom ? "点击目标节点完成连线" : "点击源节点（联系人从它出发）"} · 按 Esc 取消
          </div>
        </div>
      ) : null}

      {busy ? (
        <div className="pointer-events-none absolute top-3 right-3">
          <div className="flex items-center gap-1.5 rounded-lg border bg-card/95 px-2.5 py-1.5 text-xs shadow-sm">
            <Loader2 className="size-3.5 animate-spin" />
            保存中
          </div>
        </div>
      ) : null}

      {graph.nodes.length === 0 ? (
        <div className="pointer-events-none absolute bottom-3 left-1/2 -translate-x-1/2">
          <div className="flex items-center gap-1.5 rounded-lg border bg-card/95 px-3 py-1.5 text-muted-foreground text-xs shadow-sm">
            <Crosshair className="size-3.5" />
            还没有节点，点工具栏「从资产生成初始拓扑」或「导入 JSON」
          </div>
        </div>
      ) : null}
    </div>
  );
}
