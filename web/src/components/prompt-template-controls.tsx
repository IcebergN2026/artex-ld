/**
 * prompt-template-controls.tsx：提示词模板选择 + 就地覆盖控件，dedup 合并后通过 adapter 同时服务 planner 与 worker 两域。
 * 主要函数/类型：
 *   - PromptTemplateControls：通用模板选择控件（adapter 注入列表端点与文案）
 *   - PlannerTemplateControls / WorkerTemplateControls：planner / worker 域入口，绑定各自 adapter
 *   - PromptTemplateControlsAdapter：域差异注入接口
 */
"use client";

import * as React from "react";

import { LibraryIcon, Settings2Icon } from "lucide-react";
import { toast } from "sonner";

import { PlannerTemplateManager, WorkerTemplateManager } from "@/components/prompt-template-manager";
import { Button } from "@/components/ui/button";
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
} from "@/components/ui/combobox";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { PlannerTemplate } from "@/lib/types";

// PromptTemplateControlsAdapter 注入 planner/worker 模板控件的域差异：模板列表端点、
// 就地管理面板组件与全部文案。两域控件逻辑此前逐行相同。
interface PromptTemplateControlsAdapter {
  // 模板列表加载端点。
  list: () => Promise<PlannerTemplate[]>;
  // 加载失败 toast 前缀（如「加载规划模板失败」）。
  loadErrorPrefix: string;
  // 模板选择控件的 label / id / 空态 / 说明。
  pickerLabel: string;
  pickerId: string;
  emptyText: string;
  pickerDescription: string;
  // 提示词覆盖输入框的 label / id / 占位 / 说明。
  overrideLabel: string;
  overrideId: string;
  overridePlaceholder: string;
  overrideDescription: string;
  // 就地打开的模板管理面板组件。
  Manager: React.ComponentType<{ open: boolean; onOpenChange: (open: boolean) => void }>;
}

interface PromptTemplateControlsProps {
  templateID: number | null; // 选中的模板；null=跟随模板库默认
  onTemplateIDChange: (id: number | null) => void;
  prompt: string; // 提示词覆盖；''=不覆盖（选模板后自动灌入正文，可再手改）
  onPromptChange: (v: string) => void;
  adapter: PromptTemplateControlsAdapter;
  portalContainer?: React.RefObject<HTMLDivElement | null>; // Combobox 弹层挂点（Sheet 内滚动容器）
  // 扩展区：宿主注入的额外控件（如 planner 的 Worker 数量输入），渲染在覆盖框之后、管理面板之前。
  children?: React.ReactNode;
}

export function PromptTemplateControls({
  templateID,
  onTemplateIDChange,
  prompt,
  onPromptChange,
  adapter,
  portalContainer,
  children,
}: PromptTemplateControlsProps) {
  const [templates, setTemplates] = React.useState<PlannerTemplate[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [inputValue, setInputValue] = React.useState("");
  // 【二开·P-TPL·U9】起 —— 模板管理面板开合状态（就地管理，不再跳页）
  const [mgrOpen, setMgrOpen] = React.useState(false);
  // 【二开·P-TPL·U9】止

  const loadTemplates = React.useCallback(() => {
    adapter
      .list()
      .then(setTemplates)
      .catch((e) => {
        setTemplates([]);
        toast.error(adapter.loadErrorPrefix + "：" + (e as Error).message);
      })
      .finally(() => setLoading(false));
  }, [adapter]);

  React.useEffect(() => {
    loadTemplates();
  }, [loadTemplates]);

  const selectedTemplate = React.useMemo(
    () => templates.find((t) => t.id === templateID) ?? null,
    [templateID, templates],
  );

  React.useEffect(() => {
    setInputValue(selectedTemplate?.name ?? "");
  }, [selectedTemplate]);

  // 选中模板 = 正文灌入覆盖框（模板打底、可再手改，提交时手改优先——K22）；
  // 清空选择 = 仅解除关联，不动已灌文本（用户可能已在微调）。
  const chooseTemplate = (tpl: PlannerTemplate | null) => {
    if (!tpl) {
      setInputValue("");
      onTemplateIDChange(null);
      return;
    }
    setInputValue(tpl.name);
    onTemplateIDChange(tpl.id);
    onPromptChange(tpl.body);
    toast.success(`已灌入模板「${tpl.name}」，可在下方微调`);
  };

  let pickerPlaceholder = loading ? "正在加载模板" : adapter.emptyText;
  if (!loading && templates.length > 0) pickerPlaceholder = "默认：跟随模板库默认模板";

  return (
    <>
      {/* 控件 1：模板选择 + 就地管理入口 */}
      <Field>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FieldLabel htmlFor={adapter.pickerId}>{adapter.pickerLabel}</FieldLabel>
          {/* 【二开·P-TPL·U9】起 —— 就地打开模板管理面板（原跳转 /system/agents 再点 chip 两步操作）；关闭后刷新列表让新建立即可选 */}
          <Button type="button" variant="ghost" size="sm" onClick={() => setMgrOpen(true)}>
            <Settings2Icon data-icon="inline-start" />
            管理
          </Button>
          {/* 【二开·P-TPL·U9】止 */}
        </div>
        <Combobox
          items={templates}
          itemToStringLabel={(t) => t.name}
          itemToStringValue={(t) => String(t.id)}
          inputValue={inputValue}
          onInputValueChange={(value) => setInputValue(value)}
          value={selectedTemplate}
          onValueChange={chooseTemplate}
        >
          <ComboboxInput
            id={adapter.pickerId}
            className="w-full"
            placeholder={pickerPlaceholder}
            disabled={loading || templates.length === 0}
            showClear
          />
          <ComboboxContent portalContainer={portalContainer}>
            <ComboboxEmpty>没有匹配的模板</ComboboxEmpty>
            <ComboboxList>
              {(t) => (
                <ComboboxItem key={t.id} value={t}>
                  <LibraryIcon />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">
                      {t.name}
                      {t.is_default ? "（默认）" : ""}
                    </p>
                    <p className="text-muted-foreground truncate text-xs">{t.body}</p>
                  </div>
                </ComboboxItem>
              )}
            </ComboboxList>
          </ComboboxContent>
        </Combobox>
        <FieldDescription>{adapter.pickerDescription}</FieldDescription>
      </Field>

      {/* 控件 2：提示词覆盖（等宽 Textarea，含变量提示） */}
      <Field>
        <FieldLabel htmlFor={adapter.overrideId}>{adapter.overrideLabel}</FieldLabel>
        <Textarea
          id={adapter.overrideId}
          className="bg-muted/40 min-h-32 resize-y font-mono text-xs leading-relaxed"
          placeholder={adapter.overridePlaceholder}
          value={prompt}
          onChange={(e) => onPromptChange(e.target.value)}
        />
        <FieldDescription>{adapter.overrideDescription}</FieldDescription>
      </Field>

      {/* 扩展区（如 planner 的 Worker 数量控件） */}
      {children}

      {/* 【二开·P-TPL·U9】起 —— 模板管理面板（Sheet 内再开 Sheet 走 portal，互不干扰）；关闭后刷新列表 */}
      <adapter.Manager
        open={mgrOpen}
        onOpenChange={(o) => {
          setMgrOpen(o);
          if (!o) loadTemplates();
        }}
      />
      {/* 【二开·P-TPL·U9】止 */}
    </>
  );
}

// ── planner / worker 适配器与固定出口 ──────────────────────────────────────────
// planner 的 Worker 数量输入走 children 扩展区；其余逻辑两域逐行相同。

const plannerControlsAdapter: PromptTemplateControlsAdapter = {
  list: () => api.plannerTemplates(),
  loadErrorPrefix: "加载规划模板失败",
  pickerLabel: "规划模板（planner）",
  pickerId: "planner-template-picker",
  emptyText: "暂无规划模板",
  pickerDescription:
    "选择 planner 的规划风格模板；选择后正文自动灌入下方「规划提示词覆盖」，可再手改。优先级：手改覆盖 > 所选模板 > 默认模板。",
  overrideLabel: "规划提示词覆盖（可选）",
  overrideId: "planner-prompt-override",
  overridePlaceholder:
    "留空 = 使用上方所选模板/默认模板原文；选模板后自动灌入，可在此微调（Go template，可用变量 {{.Goal}} {{.Scope}} {{.AssetSummary}} {{.DataDir}} {{.Now}}）",
  overrideDescription:
    "全文替换 planner 提示词正文（段 [A]），仅本任务生效；代码占有的尾部规约（流量工具/中间产物输出规约）不可覆盖。",
  Manager: PlannerTemplateManager,
};

const workerControlsAdapter: PromptTemplateControlsAdapter = {
  list: () => api.workerTemplates(),
  loadErrorPrefix: "加载 Worker 模板失败",
  pickerLabel: "Worker 模板（worker）",
  pickerId: "worker-template-picker",
  emptyText: "暂无 Worker 模板",
  pickerDescription:
    "选择 Worker 的执行提示词模板；选择后正文自动灌入下方「Worker 提示词覆盖」，可再手改。优先级：手改覆盖 > 所选模板 > 默认模板。",
  overrideLabel: "Worker 提示词覆盖（可选）",
  overrideId: "worker-prompt-override",
  overridePlaceholder:
    "留空 = 使用上方所选模板/默认模板原文；选模板后自动灌入，可在此微调（Go template，可用变量 {{.WorkerName}} {{.ProxyAddr}} {{.DataDir}} {{.Now}}）",
  overrideDescription:
    "全文替换 Worker 提示词正文，仅本任务生效；代码占有的尾部规约（流量工具/中间产物输出规约）不可覆盖。",
  Manager: WorkerTemplateManager,
};

interface PlannerTemplateControlsProps {
  plannerTemplateID: number | null; // 选中的规划模板；null=跟随模板库默认
  onPlannerTemplateIDChange: (id: number | null) => void;
  plannerPrompt: string; // 规划提示词覆盖；''=不覆盖（选模板后自动灌入正文，可再手改）
  onPlannerPromptChange: (v: string) => void;
  workerCount: string; // 输入态字符串；''=0=跟随全局默认
  onWorkerCountChange: (v: string) => void;
  portalContainer?: React.RefObject<HTMLDivElement | null>; // Combobox 弹层挂点（Sheet 内滚动容器）
}

export function PlannerTemplateControls({
  plannerTemplateID,
  onPlannerTemplateIDChange,
  plannerPrompt,
  onPlannerPromptChange,
  workerCount,
  onWorkerCountChange,
  portalContainer,
}: PlannerTemplateControlsProps) {
  return (
    <PromptTemplateControls
      templateID={plannerTemplateID}
      onTemplateIDChange={onPlannerTemplateIDChange}
      prompt={plannerPrompt}
      onPromptChange={onPlannerPromptChange}
      adapter={plannerControlsAdapter}
      portalContainer={portalContainer}
    >
      {/* 控件 3：Worker 数量（0~16，0=跟随全局） */}
      <Field>
        <FieldLabel htmlFor="worker-count">Worker 数量</FieldLabel>
        <Input
          id="worker-count"
          type="number"
          min={0}
          max={16}
          className="w-40"
          placeholder="0 = 跟随全局"
          value={workerCount}
          onChange={(e) => onWorkerCountChange(e.target.value)}
        />
        <FieldDescription>
          本任务并行执行的 worker 数（1~16）；0/留空 = 跟随全局默认。多 worker 适合大范围扫描类任务，单 worker
          适合精细利用。
        </FieldDescription>
      </Field>
    </PromptTemplateControls>
  );
}

interface WorkerTemplateControlsProps {
  workerTemplateID: number | null; // 选中的 Worker 模板；null=跟随模板库默认
  onWorkerTemplateIDChange: (id: number | null) => void;
  workerPrompt: string; // Worker 提示词覆盖；''=不覆盖（选模板后自动灌入正文，可再手改）
  onWorkerPromptChange: (v: string) => void;
  portalContainer?: React.RefObject<HTMLDivElement | null>; // Combobox 弹层挂点（Sheet 内滚动容器）
}

export function WorkerTemplateControls({
  workerTemplateID,
  onWorkerTemplateIDChange,
  workerPrompt,
  onWorkerPromptChange,
  portalContainer,
}: WorkerTemplateControlsProps) {
  return (
    <PromptTemplateControls
      templateID={workerTemplateID}
      onTemplateIDChange={onWorkerTemplateIDChange}
      prompt={workerPrompt}
      onPromptChange={onWorkerPromptChange}
      adapter={workerControlsAdapter}
      portalContainer={portalContainer}
    />
  );
}
