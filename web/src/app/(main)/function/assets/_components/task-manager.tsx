/**
 * task-manager.tsx：资产页「任务管理」右侧抽屉。
 * 主要函数/类型：
 *   - TaskManager：按状态分组(进行中/已暂停/已创建/已结束)的任务卡片列表,
 *     支持搜索(名称/#ID/描述)、暂停/继续(api.controlTask)、删除(api.deleteTask,
 *     可选连带删除名下资产);归档组默认折叠。新建任务跳转任务页。
 */
"use client";

import * as React from "react";

import { useRouter } from "next/navigation";
import { ListChecksIcon, PauseIcon, PlayIcon, PlusIcon, SearchIcon, Trash2Icon } from "lucide-react";
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
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import type { Task, TaskStatus } from "@/lib/types";
import { cn, fmtTime } from "@/lib/utils";

import { taskLabel } from "./task-tabs";

type TaskGroup = { key: string; label: string; statuses: TaskStatus[]; defaultOpen: boolean };
const GROUPS: TaskGroup[] = [
  { key: "running", label: "进行中", statuses: ["running"], defaultOpen: true },
  { key: "paused", label: "已暂停", statuses: ["paused"], defaultOpen: true },
  { key: "created", label: "已创建", statuses: ["created", "queued"], defaultOpen: false },
  { key: "finished", label: "已结束", statuses: ["done", "failed", "timeout"], defaultOpen: false },
];

const STATUS_BADGE: Record<string, { label: string; className: string }> = {
  running: { label: "进行中", className: "bg-emerald-600 text-white" },
  paused: { label: "已暂停", className: "bg-yellow-500 text-black" },
  created: { label: "已创建", className: "" },
  queued: { label: "排队中", className: "" },
  done: { label: "已完成", className: "bg-slate-500 text-white" },
  failed: { label: "失败", className: "bg-red-600 text-white" },
  timeout: { label: "超时", className: "bg-slate-500 text-white" },
};

export function TaskManager({
  open,
  onOpenChange,
  tasks,
  onChanged,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tasks: Task[];
  onChanged: () => void;
}) {
  const router = useRouter();
  const [query, setQuery] = React.useState("");
  const [busy, setBusy] = React.useState("");
  const [deleteTarget, setDeleteTarget] = React.useState<Task | null>(null);
  const [deleteAssets, setDeleteAssets] = React.useState(true);

  const q = query.trim().toLowerCase();
  const match = (t: Task) =>
    !q ||
    taskLabel(t).toLowerCase().includes(q) ||
    t.id.includes(q.replace(/^#/, "")) ||
    (t.description ?? "").toLowerCase().includes(q);

  const control = async (task: Task, action: "pause" | "resume") => {
    setBusy(task.id);
    try {
      await api.controlTask(task.id, action);
      toast.success(action === "pause" ? `已暂停「${taskLabel(task)}」` : `已继续「${taskLabel(task)}」`);
      onChanged();
    } catch (e) {
      toast.error("操作失败:" + String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setBusy(deleteTarget.id);
    try {
      const res = await api.deleteTask(deleteTarget.id, {
        delete_assets: deleteAssets,
        delete_traffic: false,
        delete_files: false,
        // 发现挂在任务上,保留会成为孤儿,随任务一并删除。
        delete_findings: true,
        delete_llm_records: false,
      });
      toast.success(
        deleteAssets && res.assets_deleted > 0
          ? `已删除任务,连带删除 ${res.assets_deleted} 条资产`
          : "已删除任务",
      );
      onChanged();
    } catch (e) {
      toast.error("删除失败:" + String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
      setDeleteTarget(null);
    }
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-xl">
        <SheetHeader className="border-b px-5 py-4">
          <SheetTitle className="flex items-center gap-2">
            <ListChecksIcon className="size-4" /> 任务管理
          </SheetTitle>
          <SheetDescription>
            管理任务生命周期与资产归属;暂停后 Worker 停止改写范围,删除任务可连带删除其名下资产。
          </SheetDescription>
        </SheetHeader>

        <div className="border-b px-5 py-3">
          <div className="relative">
            <SearchIcon className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="搜索任务名称 / #ID / 描述"
              className="h-8 pl-8 text-sm"
            />
          </div>
        </div>

        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto px-5 py-4">
          {GROUPS.map((group) => {
            const list = tasks.filter((t) => group.statuses.includes(t.status) && match(t));
            if (list.length === 0) return null;
            return (
              <Collapsible key={group.key} defaultOpen={group.defaultOpen || q !== ""}>
                <CollapsibleTrigger className="flex w-full items-center gap-2 rounded-md bg-muted/40 px-3 py-2 text-xs font-semibold">
                  <span className="transition-transform [[data-state=open]>&]:rotate-90">▸</span>
                  {group.label}
                  <span className="font-normal text-muted-foreground">{list.length} 个任务</span>
                </CollapsibleTrigger>
                <CollapsibleContent className="space-y-2 pt-2">
                  {list.map((task) => (
                    <TaskCard
                      key={task.id}
                      task={task}
                      busy={busy === task.id}
                      onControl={control}
                      onDelete={(t) => {
                        setDeleteAssets(true);
                        setDeleteTarget(t);
                      }}
                    />
                  ))}
                </CollapsibleContent>
              </Collapsible>
            );
          })}
          {tasks.filter(match).length === 0 && (
            <div className="py-10 text-center text-sm text-muted-foreground">没有匹配的任务。</div>
          )}
        </div>

        <div className="flex items-center justify-between border-t px-5 py-3">
          <Button size="sm" onClick={() => router.push("/function/tasks")}>
            <PlusIcon className="size-3.5" /> 新建任务
          </Button>
          <Button size="sm" variant="outline" onClick={() => onOpenChange(false)}>
            关闭
          </Button>
        </div>
      </SheetContent>

      <AlertDialog open={!!deleteTarget} onOpenChange={(v) => !v && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除任务「{deleteTarget ? taskLabel(deleteTarget) : ""}」?</AlertDialogTitle>
            <AlertDialogDescription>
              任务与其编排状态将被删除,该操作不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={deleteAssets} onCheckedChange={(c) => setDeleteAssets(!!c)} />
            同时删除该任务名下资产
          </label>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={confirmDelete} disabled={busy !== ""}>
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Sheet>
  );
}

function TaskCard({
  task,
  busy,
  onControl,
  onDelete,
}: {
  task: Task;
  busy: boolean;
  onControl: (task: Task, action: "pause" | "resume") => void;
  onDelete: (task: Task) => void;
}) {
  const badge = STATUS_BADGE[task.status] ?? { label: task.status, className: "" };
  const findings = task.findings;
  const findingTotal = findings ? findings.critical + findings.high + findings.medium + findings.low : 0;
  return (
    <div className="rounded-md border p-3">
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span
            className={cn(
              "size-2 shrink-0 rounded-full",
              task.status === "running" && "bg-emerald-600 ring-4 ring-emerald-600/15",
              task.status === "paused" && "bg-yellow-500",
              (task.status === "created" || task.status === "queued") && "bg-slate-300",
              (task.status === "done" || task.status === "failed" || task.status === "timeout") && "bg-slate-400",
            )}
          />
          <span className="truncate text-sm font-semibold" title={task.description}>
            {taskLabel(task)}
          </span>
          <span className="font-mono text-[10px] text-muted-foreground">#{task.id}</span>
          <Badge variant={badge.className ? "default" : "outline"} className={cn("text-[10px]", badge.className)}>
            {badge.label}
          </Badge>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {task.status === "running" && (
            <Button size="sm" variant="outline" disabled={busy} onClick={() => onControl(task, "pause")}>
              <PauseIcon className="size-3.5" /> 暂停
            </Button>
          )}
          {task.status === "paused" && (
            <Button size="sm" variant="outline" disabled={busy} onClick={() => onControl(task, "resume")}>
              <PlayIcon className="size-3.5" /> 继续
            </Button>
          )}
          <Button
            size="icon"
            variant="ghost"
            className="size-8 text-muted-foreground hover:text-destructive"
            disabled={busy}
            aria-label={`删除任务 ${taskLabel(task)}`}
            onClick={() => onDelete(task)}
          >
            <Trash2Icon className="size-3.5" />
          </Button>
        </div>
      </div>
      {task.description && (
        <p className="mt-1.5 line-clamp-2 text-xs text-muted-foreground">{task.description}</p>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
        <span>
          发现 <b className="font-medium text-foreground tabular-nums">{findingTotal}</b>
        </span>
        {findings && findings.critical + findings.high > 0 && (
          <span className="flex gap-1">
            {findings.critical > 0 && <Badge className="bg-red-600 text-[10px] text-white">严 {findings.critical}</Badge>}
            {findings.high > 0 && <Badge className="bg-orange-500 text-[10px] text-white">高 {findings.high}</Badge>}
          </span>
        )}
        <span>创建于 {fmtTime(task.created_at)}</span>
      </div>
    </div>
  );
}
