/**
 * trajectory-drawer.tsx：轨迹步骤详情抽屉。点击时间线行或 minimap 色块后
 * 在时间线右侧打开,展示单步的完整信息——比行内折叠块更适合长内容:
 *   - 思考:摘要 + 完整推理全文;
 *   - 工具调用:入参(detail,JSON 则美化)+ 配对 tool_result 的输出全文;
 *   - 结果/文本等:detail 全文;
 *   - 元信息:时间、执行者、类型、工具、所属意图(点击即按该意图过滤)、token。
 * 全文通过现有 api.activityDetail(seq, taskId) 懒加载,选中步骤变化时重新拉取。
 * 主要函数/类型：
 *   - TrajectoryDrawer:详情抽屉主组件。
 *   - prettyMaybeJSON / KIND_LABEL:入参美化与类型显示名。
 */
"use client";

import * as React from "react";

import { Loader2Icon, XIcon } from "lucide-react";

import { workerColor } from "@/components/transcript";
import { api } from "@/lib/api";
import type { Activity } from "@/lib/types";
import { cn } from "@/lib/utils";

const KIND_LABEL: Record<string, string> = {
  thinking: "思考",
  text: "文本",
  tool_use: "工具调用",
  tool_result: "工具返回",
  result: "结果",
  user: "用户",
  intent: "意图",
  round: "轮次",
  intercept_request: "审批请求",
  llm_switch: "模型切换",
  llm_failover: "模型切换",
};

// 头部类型圆点颜色(与 minimap 色点同源,视觉对应)。
const KIND_DOT: Record<string, string> = {
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

// 正文分节标题:小字 + 右侧延展分隔线。
function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-1.5 flex items-center gap-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground first:mt-0 [&:not(:first-child)]:mt-3">
      <span className="shrink-0">{children}</span>
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}

// detail 是 JSON 时美化缩进,否则原样返回(tool_use 的 detail 即入参 JSON)。
function prettyMaybeJSON(s: string): string {
  const t = s.trim();
  if (!t.startsWith("{") && !t.startsWith("[")) return s;
  try {
    return JSON.stringify(JSON.parse(t), null, 2);
  } catch {
    return s;
  }
}

function MetaRow({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <>
      <span className="text-muted-foreground">{k}</span>
      <span className="min-w-0 break-all">{children}</span>
    </>
  );
}

export function TrajectoryDrawer({
  step,
  pair,
  taskId,
  workerLabel,
  intentTitle,
  onFilterIntent,
  onClose,
}: {
  step: Activity;
  /** tool_use 配对的 tool_result(同 worker + tool_use_id);无配对为 null。 */
  pair?: Activity | null;
  taskId?: string;
  workerLabel: (w: string) => string;
  /** 所属意图的标题(payload.summary);未加载到节点列表时为 undefined。 */
  intentTitle?: string;
  /** 点击意图行 → 按该意图过滤主时间线。 */
  onFilterIntent?: (intentId: string) => void;
  onClose: () => void;
}) {
  const [detail, setDetail] = React.useState<string | null>(null);
  const [pairDetail, setPairDetail] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    setDetail(null);
    setPairDetail(null);
    setLoading(true);
    api
      .activityDetail(step.seq, taskId)
      .then((r) => alive && setDetail(r.detail ?? ""))
      .catch(() => alive && setDetail(""))
      .finally(() => alive && setLoading(false));
    if (pair) {
      api
        .activityDetail(pair.seq, taskId)
        .then((r) => alive && setPairDetail(r.detail ?? ""))
        .catch(() => alive && setPairDetail(""));
    }
    return () => {
      alive = false;
    };
  }, [step.seq, pair, taskId]);

  const full = detail ?? step.summary;
  const isTool = step.kind === "tool_use" || step.kind === "tool_result";
  const failed = step.is_error === true || pair?.is_error === true;

  return (
    <aside className="flex w-80 shrink-0 flex-col border-l bg-background">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b bg-muted/30 px-3">
        <span className={cn("shrink-0 rounded px-1 text-[9px] font-medium text-white", workerColor(step.worker))}>
          {workerLabel(step.worker)}
        </span>
        <span
          className="size-2 shrink-0 rounded-full"
          style={{ backgroundColor: KIND_DOT[step.kind] ?? "#a1a1aa" }}
          aria-hidden
        />
        <span className="text-xs font-medium">{KIND_LABEL[step.kind] ?? step.kind}</span>
        <span className="font-mono text-[10px] text-muted-foreground">#{step.seq}</span>
        <button
          type="button"
          onClick={onClose}
          className="ml-auto rounded p-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          title="关闭详情"
        >
          <XIcon className="size-3.5" />
        </button>
      </div>

      <div className="grid shrink-0 grid-cols-[3.5rem_1fr] gap-x-3 gap-y-1.5 border-b bg-muted/10 px-3 py-2.5 text-[11px]">
        <MetaRow k="时间">{step.ts ? new Date(step.ts).toLocaleString("zh-CN") : "—"}</MetaRow>
        <MetaRow k="执行者">
          {workerLabel(step.worker)}({step.worker})
        </MetaRow>
        <MetaRow k="类型">{step.kind}</MetaRow>
        {step.intent_id && (
          <MetaRow k="所属意图">
            {onFilterIntent ? (
              <button
                type="button"
                className="text-sky-600 hover:underline dark:text-sky-400"
                onClick={() => onFilterIntent(step.intent_id!)}
                title="按该意图过滤时间线"
              >
                #{step.intent_id}
              </button>
            ) : (
              `#${step.intent_id}`
            )}
            {intentTitle && <span className="ml-1 text-muted-foreground">{intentTitle}</span>}
          </MetaRow>
        )}
        {step.tool && <MetaRow k="工具">{step.tool}</MetaRow>}
        {isTool && (
          <MetaRow k="状态">
            {failed ? (
              <span className="text-red-600 dark:text-red-400">✗ 失败</span>
            ) : step.kind === "tool_use" && !pair ? (
              <span className="text-sky-600 dark:text-sky-400">● 执行中/无返回</span>
            ) : (
              <span className="text-emerald-600 dark:text-emerald-400">✓ 成功</span>
            )}
          </MetaRow>
        )}
        {step.kind === "result" && (step.input_tokens != null || step.output_tokens != null) && (
          <MetaRow k="Token">
            {(step.input_tokens ?? 0) + (step.output_tokens ?? 0)}(入 {step.input_tokens ?? 0} / 出{" "}
            {step.output_tokens ?? 0})
          </MetaRow>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2.5">
        {loading && (
          <div className="flex items-center gap-1.5 py-1 text-[11px] text-muted-foreground">
            <Loader2Icon className="size-3 animate-spin" />
            加载全文…
          </div>
        )}
        {step.kind === "thinking" ? (
          <>
            <SectionTitle>完整推理</SectionTitle>
            <pre className="whitespace-pre-wrap break-words text-xs leading-relaxed text-violet-700 dark:text-violet-300">
              {full}
            </pre>
          </>
        ) : step.kind === "tool_use" ? (
          <>
            <SectionTitle>工具入参</SectionTitle>
            <pre className="mb-3 max-h-56 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/40 p-2.5 font-mono text-[11px] leading-relaxed">
              {prettyMaybeJSON(full)}
            </pre>
            {pair && (
              <>
                <SectionTitle>工具输出</SectionTitle>
                <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/40 p-2.5 font-mono text-[11px] leading-relaxed">
                  {pairDetail ?? pair.summary}
                </pre>
              </>
            )}
          </>
        ) : (
          <pre className="whitespace-pre-wrap break-words text-xs leading-relaxed">{full}</pre>
        )}
      </div>
    </aside>
  );
}
