/**
 * topology-tab.tsx：资产覆盖/拓扑图入口面板，在覆盖视图与拓扑编辑视图间切换并记忆选择。
 * 主要函数/类型：
 *   - TopologyTab：视图切换主组件（ToggleGroup + 子面板）。
 *   - storedView / View：本地存储的视图偏好读写。
 */
"use client";

import * as React from "react";

import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

import { CoverageGraphTab } from "./coverage-graph-tab";
import { TopologyEditor } from "./topology/topology-editor";

const VIEW_KEY = "artex-topology-view";

type View = "topology" | "coverage";

function storedView(): View {
  // 入口是「资产覆盖图」tab，默认落在覆盖视图；拓扑是 tab 内的可切换模式。
  return getLocalStorageValue(VIEW_KEY) === "topology" ? "topology" : "coverage";
}

/**
 * 「资产覆盖图」tab 的内容：顶部切换「资产覆盖」（只读力导图）与「内网拓扑」（可写编辑器）。
 * 两个视图共用节点抽屉里的资产原始数据段（useIPAssetRefs / useAssetRefs）。
 */
export function TopologyTab({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  const [view, setView] = React.useState<View>(storedView);

  function changeView(next: string) {
    if (next !== "topology" && next !== "coverage") return;
    setView(next);
    setLocalStorageValue(VIEW_KEY, next);
  }

  return (
    <div className="flex flex-col gap-3">
      <ToggleGroup type="single" value={view} onValueChange={changeView} variant="outline" size="sm" spacing={0}>
        <ToggleGroupItem value="coverage">资产覆盖</ToggleGroupItem>
        <ToggleGroupItem value="topology">内网拓扑</ToggleGroupItem>
      </ToggleGroup>
      {view === "topology" ? (
        <div className="flex h-[calc(100vh-310px)] min-h-[560px] flex-col">
          <TopologyEditor taskId={taskId} />
        </div>
      ) : (
        <CoverageGraphTab taskId={taskId} coverageEnabled={coverageEnabled} />
      )}
    </div>
  );
}
