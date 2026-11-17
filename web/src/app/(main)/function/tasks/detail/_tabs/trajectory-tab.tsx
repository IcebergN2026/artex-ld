/**
 * trajectory-tab.tsx：任务轨迹面板，单一时间线合并展示任务下所有 agent
 * (mainagent / planner / 各 work#N) 的完整活动流(思考/工具调用/文本/结果)，
 * SSE 实时追加 + 滚动到顶向上翻历史分页，支持按 agent 与步骤类型本地筛选。
 * 增强:意图归属条(哪个 Worker 执行哪个意图,点击按意图过滤)、右侧 minimap
 * 密度导航条(色块=步骤类型,点击跳转)、步骤详情抽屉(思考全文/工具入参输出)。
 * 主要函数/类型：
 *   - TrajectoryTab：轨迹面板主组件（合并时间线、筛选、滚动分页、SSE 直播）。
 *   - groupOfKind / KIND_GROUPS：步骤类型 → 筛选分组的映射。
 *   - sortWorkers：agent chip 的固定排序；显示名 workerLabel 复用 transcript 的共享映射。
 *   - Transcript 传 showWorkers：单 lane(如任务只有 planner)时每行仍显示执行者 chip。
 *   - intentTitle / INTENT_STATE_DOT / INTENT_STATE_TEXT：意图标题解析与状态显示。
 */
"use client";

import * as React from "react";

import { Loader2Icon, RadioIcon, RotateCwIcon, WifiOffIcon } from "lucide-react";
import { toast } from "sonner";

import { Transcript, workerColor, workerLabel } from "@/components/transcript";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { mergeActivities } from "@/lib/activity-merge";
import { api, sseUrl } from "@/lib/api";
import type { Activity, TaskNode } from "@/lib/types";
import { cn } from "@/lib/utils";

import { TrajectoryDrawer } from "./trajectory-drawer";
import { TrajectoryMinimap } from "./trajectory-minimap";

const PAGE = 200;

// ── 步骤类型筛选分组 ─────────────────────────────────────────────────────────
// usage 恒过滤（Transcript 也不渲染），未列出的 kind 归入"系统"。
const KIND_GROUPS = ["thinking", "text", "tool", "result", "system"] as const;
type KindGroup = (typeof KIND_GROUPS)[number];

const KIND_LABELS: Record<KindGroup, string> = {
  thinking: "思考",
  text: "文本",
  tool: "工具",
  result: "结果",
  system: "系统",
};

function groupOfKind(kind: string): KindGroup {
  switch (kind) {
    case "thinking":
      return "thinking";
    case "text":
      return "text";
    case "tool_use":
    case "tool_result":
      return "tool";
    case "result":
      return "result";
    default:
      return "system"; // round / user / intent / intercept_request / llm_switch / llm_failover / …
  }
}

// ── agent chip 排序与显示名 ──────────────────────────────────────────────────
const WORKER_ORDER: Record<string, number> = { mainagent: 0, planner: 1, review: 2 };

function sortWorkers(workers: string[]): string[] {
  const rank = (w: string): number => {
    if (w in WORKER_ORDER) return WORKER_ORDER[w];
    const m = /^work#(\d+)$/.exec(w);
    if (m) return 3 + Number(m[1]);
    return 100;
  };
  return workers.sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
}

// ── 意图标题与状态显示 ───────────────────────────────────────────────────────
// TaskNode.payload 是 JSONB 原文,标题取 summary 键;解析失败退化为原文截断。
function intentTitle(n: TaskNode): string {
  try {
    const p = JSON.parse(n.payload ?? "{}") as Record<string, unknown>;
    const s = typeof p.summary === "string" ? p.summary.trim() : "";
    return s || (n.payload ?? "").slice(0, 60);
  } catch {
    return (n.payload ?? "").slice(0, 60);
  }
}

// 意图状态 → 归属条上的状态圆点(完整文案放 chip 的 title)。
const INTENT_STATE_DOT: Record<string, string> = {
  done: "bg-emerald-500",
  running: "bg-sky-500 animate-pulse",
  open: "bg-zinc-400",
  proposed: "bg-amber-500",
  paused: "bg-orange-400",
  blocked: "bg-red-500",
  exhausted: "bg-red-500",
  stopped: "bg-red-500",
  deleted: "bg-zinc-500",
};

const INTENT_STATE_TEXT: Record<string, string> = {
  done: "已完成",
  running: "运行中",
  open: "待领取",
  proposed: "待审核",
  paused: "已暂停",
  blocked: "受阻",
  exhausted: "耗尽",
  stopped: "已停止",
  deleted: "已删除",
};

export function TrajectoryTab({ taskId }: { taskId: string }) {
  const [items, setItems] = React.useState<Activity[]>([]);
  const [loading, setLoading] = React.useState(true); // boot(最新一页)
  const [error, setError] = React.useState<string | undefined>(undefined);
  const [sseLive, setSseLive] = React.useState(false);
  // 空集 = 全选(不过滤);非空集 = 仅选中这些 worker。
  const [workersFilter, setWorkersFilter] = React.useState<Set<string>>(new Set());
  // 启用的类型分组;默认全开。
  const [kindsFilter, setKindsFilter] = React.useState<Set<KindGroup>>(new Set(KIND_GROUPS));
  // 意图过滤:null=不过滤;非空=仅显示该 intent 的步骤(Activity.intent_id 匹配)。
  const [intentFilter, setIntentFilter] = React.useState<string | null>(null);
  // 详情抽屉选中的步骤 seq;null=关闭。
  const [selectedSeq, setSelectedSeq] = React.useState<number | null>(null);
  // 任务意图节点(标题/状态),boot 时拉取,SSE 帧到达时节流刷新。
  const [intentNodes, setIntentNodes] = React.useState<TaskNode[]>([]);

  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const viewport = React.useCallback(
    () => (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ?? null,
    [],
  );
  // Refs backing SSE/loading without re-render churn.
  const esRef = React.useRef<EventSource | null>(null);
  const snapshotRef = React.useRef(0); // task-level snapshot cursor → SSE since=
  const atBottomRef = React.useRef(true); // transcript pinned to bottom?
  const earliestRef = React.useRef(0); // oldest loaded seq → scroll-up pagination
  const hasMoreRef = React.useRef(false);
  const loadingMoreRef = React.useRef(false);
  const reqTokenRef = React.useRef(0);
  const intentFetchRef = React.useRef(0); // 上次拉取意图节点的时间戳(10s 节流)
  const taskIdRef = React.useRef(taskId); // latest taskId → stale-response guard
  taskIdRef.current = taskId;
  const [bootToken, setBootToken] = React.useState(0);

  // ── boot：拉最新一页 → 打开任务级 SSE(snapshot cursor 无缝衔接) ─────────────
  // biome-ignore lint/correctness/useExhaustiveDependencies: bootToken 是重试计数器,本体不读取但变化必须重启 boot。
  React.useEffect(() => {
    let alive = true;
    const token = ++reqTokenRef.current;
    setLoading(true);
    setError(undefined);
    setItems([]);
    setSseLive(false);
    setWorkersFilter(new Set()); // 切换任务不沿用旧筛选(旧 worker 可能不存在)
    setKindsFilter(new Set(KIND_GROUPS));
    setIntentFilter(null); // 旧任务的意图 id 在新任务无意义
    setSelectedSeq(null); // 关闭详情抽屉
    setIntentNodes([]);
    earliestRef.current = 0;
    hasMoreRef.current = false;
    loadingMoreRef.current = false; // 旧任务的在途分页不再阻塞新任务
    atBottomRef.current = true;

    // 意图节点(标题/状态)与历史并行拉取;失败不阻塞时间线。
    intentFetchRef.current = Date.now();
    api
      .intents(taskId)
      .then((arr) => {
        if (reqTokenRef.current === token) setIntentNodes(arr);
      })
      .catch(() => {
        /* 意图列表失败不阻塞时间线 */
      });

    api
      .activityHistory(taskId, "all", 0, PAGE)
      .then((r) => {
        if (!alive || reqTokenRef.current !== token) return;
        const merged = mergeActivities([], r.items); // 返回已按 seq 升序
        snapshotRef.current = r.snapshotCursor;
        earliestRef.current = merged.length ? merged[0].seq : 0;
        hasMoreRef.current = r.hasMore;
        setItems(merged);
        setLoading(false);

        // 单一任务流:历史(id<=cursor)与直播尾巴(id>cursor)无缺口、无重叠。
        const es = new EventSource(
          sseUrl(`/api/exploration/activity/stream?task=${encodeURIComponent(taskId)}&since=${snapshotRef.current}`),
        );
        esRef.current = es;
        es.onopen = () => setSseLive(true);
        es.onerror = () => setSseLive(false); // EventSource auto-reconnects; history compensates the gap
        es.onmessage = (e) => {
          let a: Activity;
          try {
            a = JSON.parse(e.data) as Activity;
          } catch {
            return; // ignore malformed frame
          }
          setItems((prev) => mergeActivities(prev, [a]));
          // 意图状态随执行推进变化,10s 节流刷新节点列表。
          if (Date.now() - intentFetchRef.current > 10_000) {
            intentFetchRef.current = Date.now();
            api
              .intents(taskId)
              .then((arr) => {
                if (reqTokenRef.current === token) setIntentNodes(arr);
              })
              .catch(() => {
                /* 节流刷新失败忽略,下一帧再试 */
              });
          }
          if (atBottomRef.current) {
            // 底部追加后保持钉底(懒加载 detail 改变高度由 ResizeObserver 兜底)。
            requestAnimationFrame(() => {
              const vp = viewport();
              if (vp && atBottomRef.current) vp.scrollTop = vp.scrollHeight;
            });
          }
        };
      })
      .catch((err) => {
        if (!alive || reqTokenRef.current !== token) return;
        const message = (err as Error).message || "加载失败";
        setError(message);
        setLoading(false);
        toast.error(`轨迹加载失败:${message}`);
      });

    return () => {
      alive = false;
      esRef.current?.close();
      esRef.current = null;
    };
  }, [taskId, bootToken, viewport]);

  // ── 向上翻历史:到顶且还有更旧一页时加载,并保持相对滚动位置不跳动 ─────────
  const loadEarlier = React.useCallback(() => {
    if (!hasMoreRef.current || loadingMoreRef.current) return;
    loadingMoreRef.current = true;
    const forTask = taskId;
    const token = reqTokenRef.current; // boot 使旧 token 失效,在途响应不再落库
    const vp = viewport();
    const prevH = vp?.scrollHeight ?? 0;
    const prevTop = vp?.scrollTop ?? 0;
    api
      .activityHistory(taskId, "all", earliestRef.current, PAGE)
      .then((r) => {
        if (taskIdRef.current !== forTask || reqTokenRef.current !== token) return; // superseded
        setItems((prev) => {
          const merged = mergeActivities(r.items, prev);
          earliestRef.current = merged.length ? merged[0].seq : 0;
          return merged;
        });
        hasMoreRef.current = r.hasMore;
        // 内容在顶部变高:把滚动位置按增加的高度平移,视线停留在原步骤上。
        requestAnimationFrame(() => {
          const v = viewport();
          if (v) v.scrollTop = prevTop + (v.scrollHeight - prevH);
        });
      })
      .catch(() => {
        if (taskIdRef.current !== forTask || reqTokenRef.current !== token) return; // superseded
        toast.error("加载更早轨迹失败");
      })
      .finally(() => {
        if (taskIdRef.current === forTask && reqTokenRef.current === token) loadingMoreRef.current = false;
      });
  }, [taskId, viewport]);

  // 滚动监听:维护钉底状态 + 到顶翻页。
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 40;
      if (vp.scrollTop <= 80) loadEarlier(); // near top → older page
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, loadEarlier]);

  // boot / 切换任务 → 跳到最新(底部)。
  // biome-ignore lint/correctness/useExhaustiveDependencies: taskId 本体不读取,但切换任务必须重新跳底。
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [taskId, viewport]);

  // 追加/筛选变化时,钉底则保持在底。
  // biome-ignore lint/correctness/useExhaustiveDependencies: items/筛选本体不读取,其变化必须触发重新钉底。
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp && atBottomRef.current) vp.scrollTop = vp.scrollHeight;
  }, [items, workersFilter, kindsFilter, viewport]);

  // 懒加载 detail / Markdown 渲染改变内容高度时,钉底则保持在底
  // (高度变化不改变 items 引用,单靠上面的 effect 兜不住)。
  React.useEffect(() => {
    const el = contentRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      requestAnimationFrame(() => {
        const vp = viewport();
        if (vp && atBottomRef.current) vp.scrollTop = vp.scrollHeight;
      });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [viewport]);

  // ── 筛选(本地,不打请求) ─────────────────────────────────────────────────
  const workerCounts = React.useMemo(() => {
    const m = new Map<string, number>();
    for (const a of items) m.set(a.worker, (m.get(a.worker) ?? 0) + 1);
    return m;
  }, [items]);
  const workerList = React.useMemo(() => sortWorkers([...workerCounts.keys()]), [workerCounts]);

  // 过滤谓词:主时间线与 minimap 变暗共用同一份逻辑。
  const passFilter = React.useCallback(
    (a: Activity) => {
      if (a.kind === "usage") return false; // 恒过滤(Transcript 也跳过)
      if (!kindsFilter.has(groupOfKind(a.kind))) return false;
      if (workersFilter.size > 0 && !workersFilter.has(a.worker)) return false;
      if (intentFilter && a.intent_id !== intentFilter) return false;
      return true;
    },
    [kindsFilter, workersFilter, intentFilter],
  );
  const visible = React.useMemo(() => items.filter(passFilter), [items, passFilter]);

  // 意图归属:intent_id → {标题,状态}(节点列表)/ {执行 worker,步数}(活动流)。
  const intentInfo = React.useMemo(() => {
    const m = new Map<string, { title: string; state: string }>();
    for (const n of intentNodes) m.set(n.id, { title: intentTitle(n), state: n.state });
    return m;
  }, [intentNodes]);
  const intentStats = React.useMemo(() => {
    const m = new Map<string, { worker?: string; count: number }>();
    for (const a of items) {
      if (!a.intent_id) continue;
      const s = m.get(a.intent_id) ?? { count: 0 };
      s.count += 1;
      if (!s.worker && a.worker.startsWith("work#")) s.worker = a.worker;
      m.set(a.intent_id, s);
    }
    return m;
  }, [items]);
  const intentList = React.useMemo(
    () => [...intentStats.keys()].sort((a, b) => Number(a) - Number(b)),
    [intentStats],
  );

  // ── 步骤选中(详情抽屉)+ 锚点跳转 ────────────────────────────────────────
  // Transcript 每个分组根元素带 data-seq(= 组首条 seq);目标 seq 若被合并进
  // 消息组,锚点取 ≤ seq 的最近分组。
  const anchorOf = React.useCallback((seq: number): HTMLElement | undefined => {
    const root = contentRef.current;
    if (!root) return undefined;
    const anchors = [...root.querySelectorAll<HTMLElement>("[data-seq]")];
    let target: HTMLElement | undefined;
    for (const el of anchors) {
      if (Number(el.dataset.seq) <= seq) target = el;
      else break;
    }
    return target ?? anchors[0];
  }, []);

  const pickStep = React.useCallback(
    (a: Activity, scroll: boolean) => {
      setSelectedSeq(a.seq);
      if (!scroll) return;
      const vp = viewport();
      const target = anchorOf(a.seq);
      if (!vp || !target) return;
      atBottomRef.current = false; // 主动跳转后不再钉底
      const top = target.getBoundingClientRect().top - vp.getBoundingClientRect().top + vp.scrollTop;
      vp.scrollTop = Math.max(0, top - vp.clientHeight / 2);
    },
    [viewport, anchorOf],
  );

  // 选中锚点高亮:行随筛选/追加重渲染,故在每次渲染后同步 class。
  // biome-ignore lint/correctness/useExhaustiveDependencies: visible 本体不读取,其变化必须重扫锚点。
  React.useEffect(() => {
    const root = contentRef.current;
    if (!root) return;
    root.querySelectorAll(".traj-selected").forEach((el) => {
      el.classList.remove("traj-selected");
    });
    if (selectedSeq == null) return;
    anchorOf(selectedSeq)?.classList.add("traj-selected");
  }, [selectedSeq, visible, anchorOf]);

  const selected = selectedSeq != null ? (items.find((a) => a.seq === selectedSeq) ?? null) : null;
  // tool_use 的配对 tool_result(同 worker + tool_use_id),供抽屉展示出参。
  const selectedPair = React.useMemo(() => {
    if (!selected || selected.kind !== "tool_use" || !selected.tool_use_id) return null;
    return (
      items.find(
        (a) => a.kind === "tool_result" && a.worker === selected.worker && a.tool_use_id === selected.tool_use_id,
      ) ?? null
    );
  }, [items, selected]);

  const toggleWorker = (w: string) => {
    setWorkersFilter((prev) => {
      const next = new Set(prev);
      if (next.size === 0) return new Set([w]); // 全选 → 仅它
      if (next.has(w)) next.delete(w);
      else next.add(w);
      return next;
    });
  };

  const toggleKind = (g: KindGroup) => {
    setKindsFilter((prev) => {
      const next = new Set(prev);
      if (next.has(g)) next.delete(g);
      else next.add(g);
      return next;
    });
  };

  return (
    <div className="flex h-[calc(100svh-10rem)] min-h-0 flex-col gap-2 lg:h-[calc(100svh-13rem)]">
      {/* 筛选行:agent chips + 类型开关(本地过滤) */}
      <div className="flex flex-wrap items-center gap-1.5 rounded-lg border bg-card px-2 py-1.5">
        <span className="text-xs font-medium text-muted-foreground">Agent</span>
        {workerList.map((w) => {
          const active = workersFilter.size === 0 || workersFilter.has(w);
          const title =
            workersFilter.size === 0
              ? "点击仅看该 agent"
              : workersFilter.has(w) && workersFilter.size === 1
                ? "点击恢复全部"
                : workersFilter.has(w)
                  ? "取消筛选该 agent"
                  : "加入筛选";
          return (
            <button
              key={w}
              type="button"
              onClick={() => toggleWorker(w)}
              title={title}
              className={cn(
                "rounded-full border px-2 py-0.5 text-xs transition-colors",
                active
                  ? "border-primary/50 bg-primary/10 text-foreground"
                  : "border-border text-muted-foreground opacity-60 hover:opacity-100",
              )}
            >
              {workerLabel(w)}
              <span className="ml-1 text-[10px] text-muted-foreground">{workerCounts.get(w) ?? 0}</span>
            </button>
          );
        })}
        <span className="mx-1 hidden h-4 w-px bg-border sm:block" />
        <span className="text-xs font-medium text-muted-foreground">类型</span>
        {KIND_GROUPS.map((g) => {
          const active = kindsFilter.has(g);
          return (
            <button
              key={g}
              type="button"
              onClick={() => toggleKind(g)}
              className={cn(
                "rounded-full border px-2 py-0.5 text-xs transition-colors",
                active
                  ? "border-primary/50 bg-primary/10 text-foreground"
                  : "border-border text-muted-foreground opacity-60 hover:opacity-100",
              )}
            >
              {KIND_LABELS[g]}
            </button>
          );
        })}
        <span className="ml-auto flex items-center gap-1.5 text-xs text-muted-foreground">
          {loading ? (
            <Loader2Icon className="size-3.5 animate-spin" />
          ) : sseLive ? (
            <RadioIcon className="size-3.5 text-blue-500" />
          ) : (
            <WifiOffIcon className="size-3.5" />
          )}
          {sseLive ? "实时" : "离线"}
        </span>
      </div>

      {/* 意图归属条:哪个 Worker 执行哪个意图;点击按意图过滤 */}
      {intentList.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 rounded-lg border bg-card px-2 py-1.5">
          <span className="text-xs font-medium text-muted-foreground">意图</span>
          {intentList.map((id) => {
            const info = intentInfo.get(id);
            const stat = intentStats.get(id);
            const active = intentFilter === id;
            const stateText = INTENT_STATE_TEXT[info?.state ?? ""] ?? info?.state ?? "";
            return (
              <button
                key={id}
                type="button"
                onClick={() => setIntentFilter(active ? null : id)}
                title={`#${id} ${info?.title ?? ""}\n${stateText}${stat?.worker ? ` · ${workerLabel(stat.worker)}` : ""} · ${stat?.count ?? 0} 步\n${active ? "点击取消意图过滤" : "点击仅看该意图的步骤"}`}
                className={cn(
                  "flex h-6 items-center gap-1.5 rounded-md border px-2 text-[11px] transition-colors",
                  active
                    ? "border-primary/50 bg-primary/10 text-foreground"
                    : "border-border bg-muted/30 text-muted-foreground hover:bg-muted/60 hover:text-foreground",
                )}
              >
                <span className="font-mono text-[10px] font-semibold text-amber-600 dark:text-amber-400">
                  #{id}
                </span>
                {info?.title && <span className="max-w-40 truncate">{info.title}</span>}
                {stat?.worker && (
                  <span
                    className={cn("rounded px-1 text-[9px] font-medium leading-4 text-white", workerColor(stat.worker))}
                  >
                    {workerLabel(stat.worker)}
                  </span>
                )}
                <span className="flex items-center gap-1 text-[10px] text-muted-foreground">
                  <span className={cn("size-1.5 rounded-full", INTENT_STATE_DOT[info?.state ?? ""] ?? "bg-zinc-400")} />
                  {stat?.count ?? 0}步
                </span>
              </button>
            );
          })}
          {intentFilter && (
            <button
              type="button"
              onClick={() => setIntentFilter(null)}
              className="flex h-6 items-center rounded-md border border-dashed px-2 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            >
              ✕ 清除过滤
            </button>
          )}
        </div>
      )}

      {/* 合并时间线 + minimap 导航 + 详情抽屉 */}
      <div className="flex min-h-0 min-w-0 flex-1 overflow-hidden rounded-lg border bg-card">
        <ScrollArea type="auto" className="min-h-0 min-w-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!">
          {/* biome-ignore lint/a11y/noStaticElementInteractions lint/a11y/useKeyWithClickEvents: 行点击仅打开详情抽屉的便捷入口,折叠等键盘可达操作仍在各行按钮上。 */}
          <div
            className="min-w-0 max-w-full p-4"
            ref={contentRef}
            onClick={(e) => {
              // 行点击 → 打开详情抽屉(链接等交互元素除外;折叠按钮的原有行为不变)。
              const t = e.target as HTMLElement;
              if (t.closest("a")) return;
              const anchor = t.closest("[data-seq]") as HTMLElement | null;
              if (!anchor) return;
              const a = items.find((x) => x.seq === Number(anchor.dataset.seq));
              if (a) pickStep(a, false);
            }}
          >
            {error ? (
              <div className="flex flex-col items-center gap-2 py-10 text-sm text-muted-foreground">
                <span>轨迹加载失败:{error}</span>
                <Button variant="outline" size="sm" onClick={() => setBootToken((t) => t + 1)}>
                  <RotateCwIcon className="size-3.5" />
                  重试
                </Button>
              </div>
            ) : loading ? (
              <div className="flex items-center justify-center gap-2 py-10 text-xs text-muted-foreground">
                <Loader2Icon className="size-3.5 animate-spin" />
                加载轨迹…
              </div>
            ) : items.length === 0 ? (
              <div className="py-10 text-center text-xs text-muted-foreground">暂无轨迹记录</div>
            ) : visible.length === 0 ? (
              <div className="py-10 text-center text-xs text-muted-foreground">当前筛选无匹配步骤</div>
            ) : (
              <Transcript activity={visible} live={sseLive} taskId={taskId} showWorkers />
            )}
          </div>
        </ScrollArea>
        {!loading && !error && items.length > 0 && (
          <TrajectoryMinimap
            items={items}
            isVisible={passFilter}
            getViewport={viewport}
            selectedSeq={selectedSeq}
            live={sseLive}
            onPick={(a) => pickStep(a, true)}
          />
        )}
        {selected && (
          <TrajectoryDrawer
            step={selected}
            pair={selectedPair}
            taskId={taskId}
            workerLabel={workerLabel}
            intentTitle={selected.intent_id ? intentInfo.get(selected.intent_id)?.title : undefined}
            onFilterIntent={setIntentFilter}
            onClose={() => setSelectedSeq(null)}
          />
        )}
      </div>
    </div>
  );
}
