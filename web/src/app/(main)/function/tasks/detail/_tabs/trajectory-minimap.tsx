/**
 * trajectory-minimap.tsx：轨迹密度缩略导航条。把时间线每一步压成中轴导轨
 * 上的一个小圆点(颜色=步骤类型,宽度=重要度),纵向排列在滚动区右侧;
 * 色点分布本身即"轨迹指纹"(大段紫=长推理,密集蓝点=工具风暴)。功能:
 *   - 视口指示框随滚动同步(直接改 DOM,不触发重渲染);
 *   - 点击色点 → onPick(跳转 + 打开详情);点击空白 → 按位置比例滚动;
 *   - 被筛选掉的步骤色点变暗(分布仍可见);选中步骤色点加 primary 描边;
 *   - live 时底部显示绿色"实时边缘"线。
 * 主要函数/类型：
 *   - TrajectoryMinimap：缩略导航条主组件。
 *   - KIND_COLOR / KIND_TIP / KIND_W:activity kind → 颜色/悬停文案/点宽。
 */
"use client";

import * as React from "react";

import type { Activity } from "@/lib/types";
import { cn } from "@/lib/utils";

const KIND_COLOR: Record<string, string> = {
  thinking: "#8b5cf6",
  text: "#a1a1aa",
  tool_use: "#0ea5e9",
  tool_result: "#10b981",
  result: "#22c55e",
  user: "#3b82f6",
  intent: "#d97706",
  round: "#d4d4d8",
  intercept_request: "#f59e0b",
  llm_switch: "#f59e0b",
  llm_failover: "#f59e0b",
};

const KIND_TIP: Record<string, string> = {
  thinking: "思考",
  text: "文本",
  tool_use: "工具调用",
  tool_result: "工具返回",
  result: "结果",
  user: "用户",
  intent: "意图",
  round: "轮次",
  intercept_request: "审批",
  llm_switch: "模型切换",
  llm_failover: "模型切换",
};

// 点宽:结果/用户/意图更醒目,文本收敛,工具与思考常规;轮次为全宽刻度线(特判)。
const KIND_W: Record<string, number> = {
  result: 16,
  user: 16,
  intent: 16,
  intercept_request: 15,
  tool_use: 13,
  tool_result: 13,
  thinking: 13,
  llm_switch: 12,
  llm_failover: 12,
  text: 10,
};

export function TrajectoryMinimap({
  items,
  isVisible,
  getViewport,
  selectedSeq,
  live,
  onPick,
}: {
  items: Activity[];
  /** 与主时间线一致的过滤谓词;不满足的步骤色点变暗。 */
  isVisible: (a: Activity) => boolean;
  /** ScrollArea viewport 元素取值器(scroll/尺寸事件的挂载目标)。 */
  getViewport: () => HTMLElement | null;
  selectedSeq: number | null;
  live: boolean;
  /** 点击某个色点:参数为该色点对应的 activity。 */
  onPick: (a: Activity) => void;
}) {
  const trackRef = React.useRef<HTMLDivElement | null>(null);
  const viewRef = React.useRef<HTMLDivElement | null>(null);
  const steps = React.useMemo(() => items.filter((a) => a.kind !== "usage"), [items]);

  // 视口指示框:监听 viewport 滚动与内容/视口尺寸变化,直接写 style 避免重渲染。
  // biome-ignore lint/correctness/useExhaustiveDependencies: steps.length 本体不读取,内容高度变化(追加/翻页/展开)后必须重挂监听并 sync 一次。
  React.useEffect(() => {
    const vp = getViewport();
    const track = trackRef.current;
    const view = viewRef.current;
    if (!vp || !track || !view) return;
    const sync = () => {
      const total = vp.scrollHeight || 1;
      const h = track.clientHeight;
      view.style.top = `${(vp.scrollTop / total) * h}px`;
      view.style.height = `${Math.max(16, (vp.clientHeight / total) * h)}px`;
    };
    sync();
    const ro = new ResizeObserver(sync);
    ro.observe(vp);
    if (vp.firstElementChild instanceof HTMLElement) ro.observe(vp.firstElementChild);
    vp.addEventListener("scroll", sync, { passive: true });
    return () => {
      ro.disconnect();
      vp.removeEventListener("scroll", sync);
    };
  }, [getViewport, steps.length]);

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions lint/a11y/useKeyWithClickEvents: 空白区点击是比例滚动的快捷方式,色点本身是可聚焦 button,键盘可达。
    <div
      ref={trackRef}
      className="relative w-10 shrink-0 cursor-pointer border-l bg-muted/20"
      title="轨迹缩略导航:点击色点定位步骤"
      onClick={(e) => {
        // 空白区点击 → 按纵向位置比例滚动(色点自身 stopPropagation)。
        const track = trackRef.current;
        const vp = getViewport();
        if (!track || !vp) return;
        const r = track.getBoundingClientRect();
        vp.scrollTop = ((e.clientY - r.top) / r.height) * vp.scrollHeight - vp.clientHeight / 2;
      }}
    >
      {/* 中轴导轨 */}
      <div className="pointer-events-none absolute inset-y-2 left-1/2 w-px -translate-x-1/2 bg-border" />
      {steps.map((a, i) => {
        const isRound = a.kind === "round";
        return (
          <button
            key={a.seq}
            type="button"
            title={`#${a.seq} ${a.worker} · ${KIND_TIP[a.kind] ?? a.kind}\n${a.summary.slice(0, 80)}`}
            aria-label={`步骤 #${a.seq} ${a.worker} ${KIND_TIP[a.kind] ?? a.kind}`}
            className={cn("absolute rounded-full", isRound && "opacity-60")}
            style={{
              top: `calc(${(i / Math.max(1, steps.length)) * 100}% - ${isRound ? 0.5 : 1.5}px)`,
              ...(isRound
                ? { left: 7, right: 7, height: 2 }
                : { left: "50%", width: KIND_W[a.kind] ?? 11, height: 3, transform: "translateX(-50%)" }),
              backgroundColor: KIND_COLOR[a.kind] ?? "#a1a1aa",
              opacity: isVisible(a) ? undefined : 0.12,
              boxShadow: selectedSeq === a.seq ? "0 0 0 1.5px var(--primary), 0 0 5px rgb(0 0 0 / 0.15)" : undefined,
            }}
            onClick={(e) => {
              e.stopPropagation();
              if (isVisible(a)) onPick(a);
            }}
          />
        );
      })}
      <div
        ref={viewRef}
        className="pointer-events-none absolute inset-x-[4px] rounded-md border border-primary/40 bg-primary/[0.05]"
      />
      {live && (
        <div className="pointer-events-none absolute inset-x-3 bottom-1 h-[2px] rounded-full bg-emerald-500/90 shadow-[0_0_6px_rgba(16,185,129,0.8)]" />
      )}
    </div>
  );
}
