// ═══ ARTEX 二开 · [P-TPL·U2 建任务 Worker 模板与提示词覆盖] ═══
// 功能介绍：CreateTaskSheet「高级设置」内选择 Worker 模板并灌入覆盖框（可再手改）、
//           等宽覆盖 Textarea（含变量提示）；仿制 planner-template-controls.tsx。
// 【二开·P-TPL·U9】「管理」按钮不再跳转 /system/agents，就地打开 WorkerTemplateManager 面板，
//                   关闭后自动刷新模板列表（新建的场景模板立即可选）
// 二开日期：2026-10-30
"use client";

import * as React from "react";

import { LibraryIcon, Settings2Icon } from "lucide-react";
import { toast } from "sonner";

import { WorkerTemplateManager } from "@/components/worker-template-manager";

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
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { WorkerTemplate } from "@/lib/types";

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
  const [templates, setTemplates] = React.useState<WorkerTemplate[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [inputValue, setInputValue] = React.useState("");
  // 【二开·P-TPL·U9】起 —— 模板管理面板开合状态（就地管理，不再跳页）
  const [mgrOpen, setMgrOpen] = React.useState(false);
  // 【二开·P-TPL·U9】止

  const loadTemplates = React.useCallback(() => {
    api
      .workerTemplates()
      .then(setTemplates)
      .catch((e) => {
        setTemplates([]);
        toast.error("加载 Worker 模板失败：" + (e as Error).message);
      })
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    loadTemplates();
  }, [loadTemplates]);

  const selectedTemplate = React.useMemo(
    () => templates.find((t) => t.id === workerTemplateID) ?? null,
    [workerTemplateID, templates],
  );

  React.useEffect(() => {
    setInputValue(selectedTemplate?.name ?? "");
  }, [selectedTemplate]);

  // 选中模板 = 正文灌入覆盖框（模板打底、可再手改，提交时手改优先——K22）；
  // 清空选择 = 仅解除关联，不动已灌文本（用户可能已在微调）。
  const chooseTemplate = (tpl: WorkerTemplate | null) => {
    if (!tpl) {
      setInputValue("");
      onWorkerTemplateIDChange(null);
      return;
    }
    setInputValue(tpl.name);
    onWorkerTemplateIDChange(tpl.id);
    onWorkerPromptChange(tpl.body);
    toast.success(`已灌入模板「${tpl.name}」，可在下方微调`);
  };

  let pickerPlaceholder = loading ? "正在加载模板" : "暂无 Worker 模板";
  if (!loading && templates.length > 0) pickerPlaceholder = "默认：跟随模板库默认模板";

  return (
    <>
      {/* Worker 模板选择 + 就地管理入口 */}
      <Field>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FieldLabel htmlFor="worker-template-picker">Worker 模板（worker）</FieldLabel>
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
            id="worker-template-picker"
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
        <FieldDescription>
          选择 Worker 的执行提示词模板；选择后正文自动灌入下方「Worker 提示词覆盖」，可再手改。优先级：手改覆盖
          &gt; 所选模板 &gt; 默认模板。
        </FieldDescription>
      </Field>

      {/* 控件 2：Worker 提示词覆盖（等宽 Textarea，含变量提示） */}
      <Field>
        <FieldLabel htmlFor="worker-prompt-override">Worker 提示词覆盖（可选）</FieldLabel>
        <Textarea
          id="worker-prompt-override"
          className="bg-muted/40 min-h-32 resize-y font-mono text-xs leading-relaxed"
          placeholder="留空 = 使用上方所选模板/默认模板原文；选模板后自动灌入，可在此微调（Go template，可用变量 {{.WorkerName}} {{.ProxyAddr}} {{.DataDir}} {{.Now}}）"
          value={workerPrompt}
          onChange={(e) => onWorkerPromptChange(e.target.value)}
        />
        <FieldDescription>
          全文替换 Worker 提示词正文，仅本任务生效；代码占有的尾部规约（流量工具/中间产物输出规约）不可覆盖。
        </FieldDescription>
      </Field>

      {/* 【二开·P-TPL·U9】起 —— 模板管理面板（Sheet 内再开 Sheet 走 portal，互不干扰）；关闭后刷新列表 */}
      <WorkerTemplateManager
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
