// ═══ ARTEX 二开 · [P-TPL·U8 planner模板管理面板] ═══
// 功能介绍：agents 页 planner 瓦片「提示词：模板库 →」chip 打开的 Sheet 管理面板——左侧模板列表
//           （内置默认置顶带徽章）+ 右侧编辑表单（名称/正文/变量 chips/设为默认/试渲染校验）+ 底部
//           删除（内置/默认禁用，AlertDialog 二次确认）；骨架 1:1 仿 task-template-controls.tsx 的
//           TaskTemplateManager，字段裁剪为 prompt-only（无描述/目标/分类/拦截规则）
// 二开日期：2026-10-21
"use client";

import * as React from "react";

import { LibraryIcon, PlayIcon, PlusIcon, SaveIcon, Trash2Icon } from "lucide-react";
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { PlannerTemplate } from "@/lib/types";
import { cn } from "@/lib/utils";

// 合法变量目录 = 00 册 A4 PlannerVars 五字段（agent/prompt.go:18）；与后端 S11 试渲染同目录。
const PLANNER_VARS = ["{{.Goal}}", "{{.Scope}}", "{{.AssetSummary}}", "{{.DataDir}}", "{{.Now}}"] as const;

interface PlannerTemplateDraft {
  name: string;
  body: string;
  isDefault: boolean;
}

interface PlannerTemplateManagerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

const emptyDraft = (): PlannerTemplateDraft => ({ name: "", body: "", isDefault: false });

function templateDraft(template: PlannerTemplate): PlannerTemplateDraft {
  return { name: template.name, body: template.body, isDefault: template.is_default };
}

// 列表排序：内置默认置顶，其后默认模板，其余保持后端 updated_at DESC 顺序（sort 稳定性保证）。
function sortTemplates(list: PlannerTemplate[]): PlannerTemplate[] {
  return [...list].sort((a, b) => Number(b.builtin) - Number(a.builtin) || Number(b.is_default) - Number(a.is_default));
}

export function PlannerTemplateManager({ open, onOpenChange }: PlannerTemplateManagerProps) {
  const [templates, setTemplates] = React.useState<PlannerTemplate[]>([]);
  const [selectedID, setSelectedID] = React.useState<number | null>(null); // null=新建草稿
  const [draft, setDraft] = React.useState<PlannerTemplateDraft>(emptyDraft);
  const [saving, setSaving] = React.useState(false);
  const [validating, setValidating] = React.useState(false);
  const [validateErr, setValidateErr] = React.useState(""); // 试渲染 400 错误详情（含模板行号）红字回显
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);
  const bodyRef = React.useRef<HTMLTextAreaElement>(null);
  const wasOpen = React.useRef(false);

  // 保存/删除后刷新列表（不动当前选中与草稿）。
  const reload = React.useCallback(async () => {
    try {
      setTemplates(sortTemplates(await api.plannerTemplates()));
    } catch (error) {
      toast.error(`刷新模板列表失败：${(error as Error).message}`);
    }
  }, []);

  // 每次打开面板：拉取列表并默认选中「默认模板」（无默认则列表首个，通常即内置默认）。
  React.useEffect(() => {
    if (!open) {
      wasOpen.current = false;
      return;
    }
    if (wasOpen.current) return;
    wasOpen.current = true;
    setValidateErr("");
    api
      .plannerTemplates()
      .then((list) => {
        const sorted = sortTemplates(list);
        setTemplates(sorted);
        const preferred = sorted.find((t) => t.is_default) ?? sorted[0];
        if (preferred) {
          setSelectedID(preferred.id);
          setDraft(templateDraft(preferred));
        } else {
          setSelectedID(null);
          setDraft(emptyDraft());
        }
      })
      .catch((error) => {
        setTemplates([]);
        toast.error(`加载模板失败：${(error as Error).message}`);
      });
  }, [open]);

  const selected = React.useMemo(
    () => templates.find((t) => t.id === selectedID) ?? null,
    [selectedID, templates],
  );
  const readOnly = !!selected?.builtin; // 内置模板名称/正文只读（保 seed 幂等身份，D11-c Patch 守护）
  // 内置禁删（D11-c）、默认禁删（ErrPlannerTemplateDefaultProtected）——前端先拦，后端 409 兜底。
  const deleteDisabled = !selected || selected.builtin || selected.is_default;

  const selectTemplate = (template: PlannerTemplate) => {
    setSelectedID(template.id);
    setDraft(templateDraft(template));
    setValidateErr("");
  };

  const startNew = () => {
    const builtin = templates.find((t) => t.builtin);
    setSelectedID(null);
    setDraft({ name: "", body: builtin?.body ?? "", isDefault: false }); // 以内置默认正文为底稿
    setValidateErr("");
  };

  // 变量 chip：插入到正文光标处（内置只读时禁用）。
  const insertVar = (v: string) => {
    const ta = bodyRef.current;
    if (!ta || readOnly) return;
    const s = ta.selectionStart ?? draft.body.length;
    const e = ta.selectionEnd ?? draft.body.length;
    setDraft((cur) => ({ ...cur, body: cur.body.slice(0, s) + v + cur.body.slice(e) }));
    requestAnimationFrame(() => {
      ta.focus();
      ta.selectionStart = ta.selectionEnd = s + v.length;
    });
  };

  // 试渲染校验：调 S11-v 干跑端点（不落库）；成功 toast，失败错误详情（含模板行号）红字回显。
  async function validate() {
    setValidating(true);
    setValidateErr("");
    try {
      await api.validatePlannerTemplate(draft.body);
      toast.success("试渲染通过：语法正确、变量均在目录内");
    } catch (error) {
      setValidateErr((error as Error).message);
    } finally {
      setValidating(false);
    }
  }

  async function save() {
    const input = { name: draft.name.trim(), body: draft.body.trim() };
    if (!input.name || !input.body) {
      toast.error("请填写模板名称与提示词正文");
      return;
    }
    setSaving(true);
    try {
      let savedID = selectedID;
      if (selectedID == null) {
        const created = await api.createPlannerTemplate(input);
        savedID = created.id;
        if (draft.isDefault) await api.setDefaultPlannerTemplate(created.id);
        toast.success(`模板「${created.name}」已创建`);
      } else {
        const updated = await api.updatePlannerTemplate(selectedID, input);
        if (draft.isDefault && !updated.is_default) await api.setDefaultPlannerTemplate(updated.id);
        toast.success(`模板「${updated.name}」已保存`);
      }
      await reload();
      setSelectedID(savedID);
      setValidateErr("");
    } catch (error) {
      // S11 保存时同样试渲染：400 错误文本自带模板行号（template: p:<行>:<列> …）
      toast.error(`保存失败：${(error as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (selectedID == null) return;
    const deletedID = selectedID;
    setDeleting(true);
    try {
      await api.deletePlannerTemplate(deletedID);
      const rest = templates.filter((t) => t.id !== deletedID);
      setTemplates(rest);
      const next = rest.find((t) => t.is_default) ?? rest[0];
      if (next) {
        setSelectedID(next.id);
        setDraft(templateDraft(next));
      } else {
        setSelectedID(null);
        setDraft({ name: "", body: "", isDefault: false });
      }
      setDeleteOpen(false);
      toast.success("模板已删除");
    } catch (error) {
      toast.error(`删除失败：${(error as Error).message}`);
    } finally {
      setDeleting(false);
    }
  }

  let saveLabel = saving ? "保存中" : "保存修改";
  if (!saving && selectedID == null) saveLabel = "创建模板";

  let deleteHint = "";
  if (selected?.builtin) deleteHint = "内置模板为只读，不可删除";
  else if (selected?.is_default) deleteHint = "默认模板不能删除，请先在另一模板上设默认";

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent className="grid h-full w-full! max-w-none! grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:w-[48rem]! sm:max-w-[48rem]!">
          <SheetHeader className="border-b px-6 py-5">
            <SheetTitle>planner模板管理</SheetTitle>
            <SheetDescription>
              模板保存 planner 提示词正文；修改不会影响已经创建的任务（快照语义）。
            </SheetDescription>
          </SheetHeader>
          <div className="grid min-h-0 overflow-y-auto lg:grid-cols-[15rem_minmax(0,1fr)] lg:overflow-hidden">
            <div className="flex min-h-0 flex-col border-b p-3 lg:border-r lg:border-b-0">
              <Button type="button" variant="outline" className="w-full" onClick={startNew}>
                <PlusIcon data-icon="inline-start" />
                新建模板
              </Button>
              <ScrollArea className="mt-2 max-h-44 lg:max-h-none lg:flex-1">
                <div className="flex flex-col gap-1 pr-2">
                  {templates.length === 0 && (
                    <p className="text-muted-foreground px-2 py-6 text-center text-sm">暂无模板</p>
                  )}
                  {templates.map((template) => (
                    <button
                      key={template.id}
                      type="button"
                      className={cn(
                        "min-w-0 rounded-md px-2.5 py-2 text-left transition-colors",
                        selectedID === template.id ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
                      )}
                      onClick={() => selectTemplate(template)}
                    >
                      <span className="flex items-center gap-1.5">
                        <LibraryIcon className="text-muted-foreground size-3.5 shrink-0" />
                        <span className="truncate text-sm font-medium">{template.name}</span>
                        {template.builtin && (
                          <Badge variant="secondary" className="shrink-0 px-1.5 py-0 text-[10px]">
                            内置
                          </Badge>
                        )}
                        {template.is_default && (
                          <Badge
                            variant="secondary"
                            className="shrink-0 bg-green-100 px-1.5 py-0 text-[10px] text-green-800"
                          >
                            默认
                          </Badge>
                        )}
                      </span>
                      <span className="text-muted-foreground block truncate font-mono text-xs">
                        {template.body.split("\n", 1)[0]}
                      </span>
                    </button>
                  ))}
                </div>
              </ScrollArea>
            </div>
            <ScrollArea className="min-h-0">
              <FieldGroup className="p-6">
                <Field>
                  <FieldLabel htmlFor="planner-template-name">模板名称</FieldLabel>
                  <Input
                    id="planner-template-name"
                    value={draft.name}
                    maxLength={120}
                    placeholder="例如：内网横向·谨慎"
                    disabled={readOnly}
                    onChange={(event) => setDraft((cur) => ({ ...cur, name: event.target.value }))}
                  />
                  <FieldDescription>
                    名称归一化后唯一（防视觉等价重名）。
                    {readOnly && "内置模板为只读，可点左侧「新建模板」以它为正本底稿另建。"}
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="planner-template-body">
                    提示词正文（Go template，全文替换 planner 提示词段 [A]）
                  </FieldLabel>
                  <Textarea
                    id="planner-template-body"
                    ref={bodyRef}
                    className="bg-muted/40 min-h-72 resize-y font-mono text-xs leading-relaxed"
                    value={draft.body}
                    readOnly={readOnly}
                    onChange={(event) => setDraft((cur) => ({ ...cur, body: event.target.value }))}
                  />
                  {validateErr && <p className="text-destructive text-xs whitespace-pre-wrap">{validateErr}</p>}
                  <FieldDescription>
                    代码占有的尾部规约（流量工具/中间产物输出规约）在渲染后追加，不在此列、不可覆盖。
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel>变量目录（点击插入到正文光标处）</FieldLabel>
                  <div className="flex flex-wrap gap-1.5">
                    {PLANNER_VARS.map((v) => (
                      <button
                        key={v}
                        type="button"
                        disabled={readOnly}
                        onClick={() => insertVar(v)}
                        className={cn(
                          "bg-accent rounded border px-1.5 py-0.5 font-mono text-[11px]",
                          readOnly ? "opacity-50" : "hover:border-foreground cursor-pointer",
                        )}
                      >
                        {v}
                      </button>
                    ))}
                  </div>
                  <FieldDescription>
                    仅这五个变量可用；引用目录外变量保存/试渲染会被拒（400 带模板行号）。
                  </FieldDescription>
                </Field>
                <Field>
                  <div className="flex items-center gap-2">
                    <Switch
                      id="planner-template-default"
                      checked={draft.isDefault}
                      disabled={selected?.is_default} // 默认模板不能在此取消默认——在另一模板上设默认即自动让位
                      onCheckedChange={(v) => setDraft((cur) => ({ ...cur, isDefault: !!v }))}
                    />
                    <FieldLabel htmlFor="planner-template-default" className="font-normal">
                      设为默认模板
                    </FieldLabel>
                  </div>
                  <FieldDescription>「默认」全库唯一；建任务未选择模板时，用默认模板正文快照打底。</FieldDescription>
                </Field>
                <Field>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="w-fit"
                    disabled={validating || readOnly}
                    onClick={() => void validate()}
                  >
                    {validating ? <Spinner data-icon="inline-start" /> : <PlayIcon data-icon="inline-start" />}
                    {validating ? "校验中" : "试渲染校验"}
                  </Button>
                  <FieldDescription>
                    不保存、不落库，仅调试渲染端点干跑：成功弹 toast，失败在正文下方红字给出模板行号。
                  </FieldDescription>
                </Field>
              </FieldGroup>
            </ScrollArea>
          </div>
          <SheetFooter className="border-t px-6 py-4 sm:flex-row sm:items-center">
            {selectedID != null && (
              <Button
                type="button"
                variant="destructive"
                className="sm:mr-auto"
                disabled={deleteDisabled}
                title={deleteHint || undefined}
                onClick={() => setDeleteOpen(true)}
              >
                <Trash2Icon data-icon="inline-start" />
                删除模板
              </Button>
            )}
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              关闭
            </Button>
            {!readOnly && (
              <Button type="button" disabled={saving} onClick={() => void save()}>
                {saving ? <Spinner data-icon="inline-start" /> : <SaveIcon data-icon="inline-start" />}
                {saveLabel}
              </Button>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除模板「{draft.name || "未命名模板"}」？</AlertDialogTitle>
            <AlertDialogDescription>
              已选用它的任务不会受到影响——创建时模板正文已快照进任务（planner_prompt_override）。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={deleting}
              onClick={(event) => {
                event.preventDefault();
                void remove();
              }}
            >
              {deleting && <Spinner data-icon="inline-start" />}
              {deleting ? "删除中" : "删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
