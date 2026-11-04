/**
 * topology-edge-dialog.tsx：新建拓扑连线的弹窗（线型、标签、颜色选择）。
 * 主要函数/类型：
 *   - CreateEdgeDialog：表单弹窗，提交新边到后端；ConnPair：待连接源/目标 IP 对
 */
"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { TopologyEdgeInput, TopologyEdgeKind } from "@/lib/types";

import { EDGE_KIND_META, EDGE_KIND_OPTIONS } from "./graph-model";
import { ColorPicker } from "./topology-node-drawer";
import type { TopologyRun } from "./topology-toolbar";

export type ConnPair = { src: string; dst: string };

export function CreateEdgeDialog({
  taskId,
  pair,
  run,
  onClose,
}: {
  taskId: string;
  pair: ConnPair | null;
  run: TopologyRun;
  onClose: () => void;
}) {
  const [kind, setKind] = React.useState<TopologyEdgeKind>("solid");
  const [label, setLabel] = React.useState("");
  const [color, setColor] = React.useState("");

  // A new pair resets the form.
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new pair intentionally resets the form.
  React.useEffect(() => {
    setKind("solid");
    setLabel("");
    setColor("");
  }, [pair]);

  async function handleSubmit() {
    if (!pair) return;
    const body: TopologyEdgeInput = {
      src_ip: pair.src,
      dst_ip: pair.dst,
      kind,
      label: label.trim(),
    };
    if (color) body.color = color;
    const next = await run(() => api.addTopologyEdge(taskId, body), "联系已添加");
    if (next) onClose();
  }

  return (
    <Dialog
      open={pair !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>添加联系</DialogTitle>
          <DialogDescription className="font-mono text-xs">{pair ? `${pair.src} → ${pair.dst}` : ""}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="topology-edge-kind">联系类型</Label>
            <Select value={kind} onValueChange={(v) => setKind(v as TopologyEdgeKind)}>
              <SelectTrigger id="topology-edge-kind" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {EDGE_KIND_OPTIONS.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value}>
                    {opt.label} · {EDGE_KIND_META[opt.value].hint}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="topology-edge-label">联系说明</Label>
            <Input
              id="topology-edge-label"
              value={label}
              maxLength={200}
              placeholder="留空则按类型显示默认名称"
              onChange={(e) => setLabel(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void handleSubmit();
              }}
            />
            <p className="text-muted-foreground text-xs">证据引用由 Agent 写入；手工添加的联系先按所选类型展示。</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label>颜色（可选）</Label>
            <ColorPicker value={color} onChange={setColor} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button disabled={pair === null} onClick={() => void handleSubmit()}>
            添加
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
