/**
 * task-tabs.tsx：资产页的任务维度过滤条。
 * 主要函数/类型：
 *   - taskLabel：任务显示名(名称缺省回退到描述截断)。
 *   - TaskTabs：任务少时平铺页签;平铺渲染超出容器宽度时自动折叠为
 *     「选中任务 chip + 选择任务 ▾(可搜索下拉)」。ResizeObserver 驱动,双向切换。
 * value="" 表示不过滤(全部任务)。
 */
"use client";

import * as React from "react";

import { CheckIcon, ChevronDownIcon, SearchIcon, XIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Task } from "@/lib/types";
import { cn } from "@/lib/utils";

// taskLabel 是任务在过滤条/选择器中的显示名:名称缺省时回退到描述截断。
export function taskLabel(task: Task): string {
  const name = task.name?.trim();
  if (name) return name;
  const desc = task.description?.trim() ?? "";
  return desc.length > 24 ? `${desc.slice(0, 24)}…` : desc || `#${task.id}`;
}

function StatusBadge({ task }: { task: Task }) {
  return task.active ? (
    <Badge className="bg-emerald-600 text-[10px] text-white">进行中</Badge>
  ) : (
    <Badge variant="outline" className="text-[10px]">
      {task.status}
    </Badge>
  );
}

export function TaskTabs({
  tasks,
  value,
  onChange,
}: {
  tasks: Task[];
  value: string;
  onChange: (taskID: string) => void;
}) {
  const wrapRef = React.useRef<HTMLDivElement>(null);
  const measureRef = React.useRef<HTMLDivElement>(null);
  const [overflow, setOverflow] = React.useState(false);
  const [popOpen, setPopOpen] = React.useState(false);
  const [query, setQuery] = React.useState("");

  // 平铺内容放进隐藏测量容器,超出可见容器宽度即折叠;ResizeObserver 监听双向变化。
  React.useEffect(() => {
    const wrap = wrapRef.current;
    const measure = measureRef.current;
    if (!wrap || !measure) return;
    const check = () => setOverflow(measure.scrollWidth > wrap.clientWidth + 1);
    check();
    const observer = new ResizeObserver(check);
    observer.observe(wrap);
    observer.observe(measure);
    return () => observer.disconnect();
  }, [tasks]);

  // 点击弹层外部关闭。
  React.useEffect(() => {
    if (!popOpen) return;
    const close = () => setPopOpen(false);
    document.addEventListener("click", close);
    return () => document.removeEventListener("click", close);
  }, [popOpen]);

  if (tasks.length === 0) return null;

  const selected = tasks.find((t) => t.id === value) ?? null;
  const q = query.trim().toLowerCase();
  const matches = tasks.filter(
    (t) =>
      !q ||
      taskLabel(t).toLowerCase().includes(q) ||
      t.id.includes(q.replace(/^#/, "")) ||
      (t.description ?? "").toLowerCase().includes(q),
  );

  const flatTabs = (
    <Tabs value={value || "all"} onValueChange={(v) => onChange(v === "all" ? "" : v)}>
      <TabsList variant="default" className="flex-nowrap">
        <TabsTrigger value="all" className="shrink-0 whitespace-nowrap">
          全部任务
        </TabsTrigger>
        {tasks.map((t) => (
          <TabsTrigger key={t.id} value={t.id} className="shrink-0 whitespace-nowrap">
            <span className="max-w-56 truncate" title={t.name?.trim() || t.description}>
              {taskLabel(t)}
            </span>
            <span className="font-mono text-[10px] text-muted-foreground">#{t.id}</span>
            <StatusBadge task={t} />
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  );

  return (
    <div ref={wrapRef} className="relative">
      {/* 隐藏的平铺测量副本:决定是否需要折叠 */}
      <div ref={measureRef} aria-hidden className="pointer-events-none absolute inset-x-0 top-0 invisible w-max">
        {flatTabs}
      </div>

      {overflow ? (
        <div className="flex items-center gap-2">
          {selected ? (
            <span className="inline-flex h-9 items-center gap-2 rounded-md border bg-card px-3 text-sm font-medium shadow-xs">
              <span className="max-w-56 truncate">{taskLabel(selected)}</span>
              <span className="font-mono text-[10px] text-muted-foreground">#{selected.id}</span>
              <StatusBadge task={selected} />
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground"
                aria-label="清除任务过滤"
                onClick={() => onChange("")}
              >
                <XIcon className="size-3.5" />
              </button>
            </span>
          ) : (
            <span className="inline-flex h-9 items-center rounded-md bg-muted px-3 text-sm font-medium">全部任务</span>
          )}
          <div className="relative">
            <button
              type="button"
              className="inline-flex h-9 items-center gap-1.5 rounded-md border bg-card px-3 text-sm text-muted-foreground shadow-xs hover:text-foreground"
              onClick={(e) => {
                e.stopPropagation();
                setPopOpen((v) => !v);
              }}
            >
              选择任务 <ChevronDownIcon className="size-3.5" />
              <span className="font-mono text-[10px]">{tasks.length} 个</span>
            </button>
            {popOpen && (
              <div
                className="absolute left-0 top-full z-30 mt-1.5 w-80 rounded-md border bg-card p-2 shadow-lg"
                onClick={(e) => e.stopPropagation()}
              >
                <div className="relative mb-1.5">
                  <SearchIcon className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    autoFocus
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    placeholder="搜索任务名称 / #ID / 描述"
                    className="h-8 pl-8 text-sm"
                  />
                </div>
                <div className="max-h-72 overflow-y-auto">
                  <PickerOption
                    label="全部任务"
                    selected={value === ""}
                    onSelect={() => {
                      onChange("");
                      setPopOpen(false);
                      setQuery("");
                    }}
                  />
                  {matches.map((t) => (
                    <PickerOption
                      key={t.id}
                      label={taskLabel(t)}
                      suffix={`#${t.id}`}
                      badge={<StatusBadge task={t} />}
                      selected={value === t.id}
                      onSelect={() => {
                        onChange(t.id);
                        setPopOpen(false);
                        setQuery("");
                      }}
                    />
                  ))}
                  {matches.length === 0 && (
                    <div className="py-6 text-center text-xs text-muted-foreground">没有匹配的任务。</div>
                  )}
                </div>
              </div>
            )}
          </div>
        </div>
      ) : (
        <div className="overflow-x-auto overflow-y-hidden">{flatTabs}</div>
      )}
    </div>
  );
}

function PickerOption({
  label,
  suffix,
  badge,
  selected,
  onSelect,
}: {
  label: string;
  suffix?: string;
  badge?: React.ReactNode;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      className={cn(
        "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-muted",
        selected && "bg-muted font-medium",
      )}
      onClick={onSelect}
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {suffix && <span className="font-mono text-[10px] text-muted-foreground">{suffix}</span>}
      {badge}
      {selected && <CheckIcon className="size-3.5 text-muted-foreground" />}
    </button>
  );
}
