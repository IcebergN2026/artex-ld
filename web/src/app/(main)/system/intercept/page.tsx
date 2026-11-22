/**
 * page.tsx：拦截配置页——工具调用拦截规则、工具作用范围与审批 Judge 配置。
 * 主要函数/类型：
 *   - InterceptPage：默认导出页组件，规则列表 + 编辑抽屉 + 工具范围弹窗
 *   - JudgeCard：审批 Judge 开关、审批模型选择与近 30 天用量统计
 *   - groupTools / sdkTool / SDK_EXEC / SDK_WRITE / SDK_KEYS：工具分组（内置 SDK 工具硬编码）
 *   - fmtTokens / JudgeStat / JudgeSparkbars：用量数字的紧凑写法与每日迷你柱图
 *   - ActionBadge：拦截动作徽章（允许 / 禁止 / 申请）
 */
"use client";

import * as React from "react";
import { toast } from "sonner";
import {
  BotIcon,
  ListFilterIcon,
  PencilIcon,
  PlusIcon,
  ShieldAlertIcon,
  TagIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Card, CardContent } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type {
  Agent,
  InterceptAction,
  InterceptRule,
  JudgeConfig,
  JudgeDayUsage,
  JudgeUsage,
  LLMProfile,
  Tool,
} from "@/lib/types";

// fmtTokens 把 token 数压成紧凑写法(1.2k / 3.4M),用于审批用量统计。
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(1) + "k";
  return String(n);
}

// JudgeStat 是一块统计数字(标签 + 数值)。
function JudgeStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border bg-muted/20 px-3 py-2">
      <div className="text-[10px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  );
}

// JudgeSparkbars 用纯 div 画近 N 天每日消耗(输入+输出)的迷你柱图,免图表库依赖。
function JudgeSparkbars({ daily }: { daily: JudgeDayUsage[] }) {
  const max = Math.max(1, ...daily.map((d) => d.input_tokens + d.output_tokens));
  return (
    <div className="flex h-16 items-end gap-0.5">
      {daily.map((d) => {
        const total = d.input_tokens + d.output_tokens;
        const h = Math.max(2, Math.round((total / max) * 100));
        return (
          <div
            key={d.date}
            title={`${d.date} · ${d.calls} 次 · ${fmtTokens(total)} tokens`}
            className="min-w-[2px] flex-1 rounded-sm bg-violet-500/60 hover:bg-violet-500"
            style={{ height: `${h}%` }}
          />
        );
      })}
    </div>
  );
}

// ---- tool scope ----

// SDK tools are intentionally not seeded into the DB (they apply to every agent
// and have no per-agent binding). We hardcode them here so they still appear in
// the scope dialog.
function sdkTool(key: string, description: string): Tool {
  return { key, system: true, description, schema: {}, agents: [], enabled: true, kind: "builtin" };
}

const SDK_EXEC: Tool[] = [
  sdkTool("Bash",        "在 shell 中执行命令"),
  sdkTool("WebFetch",    "发起 HTTP/HTTPS 请求（含代理支持）"),
  sdkTool("web_search",  "网络搜索"),
  sdkTool("shell_open",  "开启持久 PTY 交互会话"),
  sdkTool("shell_send",  "向交互会话发送输入"),
  sdkTool("shell_read",  "读取交互会话输出"),
  sdkTool("shell_close", "关闭交互会话"),
  sdkTool("shell_list",  "列出所有交互会话"),
];

const SDK_WRITE: Tool[] = [
  sdkTool("Write",     "写入文件"),
  sdkTool("Edit",      "编辑文件（精确替换）"),
  sdkTool("MultiEdit", "批量编辑文件"),
];

const SDK_KEYS = new Set([...SDK_EXEC, ...SDK_WRITE].map((t) => t.key));

function groupTools(dbTools: Tool[]) {
  const sys: Tool[] = [], custom: Tool[] = [];
  for (const t of dbTools) {
    if (SDK_KEYS.has(t.key)) continue; // already covered by hardcoded groups
    if (t.system) sys.push(t);
    else          custom.push(t);
  }
  return [
    { label: "执行类",      tools: SDK_EXEC },
    { label: "写入/编辑类", tools: SDK_WRITE },
    { label: "系统工具",    tools: sys },
    { label: "自定义工具",  tools: custom },
  ].filter((g) => g.tools.length > 0);
}

// ---- form state ----

type RuleForm = {
  name: string;
  enabled: boolean;
  priority: number;
  match_target: "tool_name" | "tool_input";
  match_type: "string" | "regex";
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  tags: string[];
  agents: string[];
};

const defaultForm = (): RuleForm => ({
  name: "",
  enabled: true,
  priority: 0,
  match_target: "tool_name",
  match_type: "string",
  pattern: "",
  action: "deny",
  message: "",
  timeout_enabled: true,
  timeout_seconds: 60,
  timeout_action: "deny",
  tags: [],
  agents: ["all"],
});

// ---- small components ----

function ActionBadge({ action }: { action: InterceptAction }) {
  if (action === "allow") return <Badge variant="secondary">允许</Badge>;
  if (action === "deny")  return <Badge variant="destructive">禁止</Badge>;
  return <Badge variant="outline" className="border-amber-400 text-amber-600">申请</Badge>;
}

// ---- LLM fallback judge card ----

const FOLLOW_ACTIVE = "0"; // profile_id 0 = 跟随激活/默认配置

const defaultJudge = (): JudgeConfig => ({
  enabled: false,
  profile_id: 0,
  prompt: "",
  timeout_seconds: 15,
  fail_action: "allow",
  ask_timeout_seconds: 300,
  ask_timeout_action: "deny",
});

function JudgeCard() {
  const [cfg, setCfg] = React.useState<JudgeConfig>(defaultJudge());
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [usage, setUsage] = React.useState<JudgeUsage | null>(null);

  // 审批用量统计:失败不打断配置页,仅在开启时拉取。
  const loadUsage = React.useCallback(async () => {
    try {
      setUsage(await api.interceptJudgeUsage(30));
    } catch {
      // 忽略:统计不可用不应影响配置编辑
    }
  }, []);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [j, ps] = await Promise.all([api.interceptGetJudgeConfig(), api.llmProfiles()]);
      setCfg(j);
      setProfiles(ps);
    } catch (e) {
      toast.error("加载模型兜底配置失败: " + (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // 开启后(含初次加载把开关读为 true 时)拉取审批用量统计。
  React.useEffect(() => {
    if (cfg.enabled) loadUsage();
  }, [cfg.enabled, loadUsage]);

  function patch(p: Partial<JudgeConfig>) {
    setCfg((c) => ({ ...c, ...p }));
  }

  async function save() {
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig(cfg);
      toast.success("模型兜底配置已保存");
      await load(); // 回读:提示词若清空则回填内置模板
    } catch (e) {
      toast.error("保存失败: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function restorePrompt() {
    // 清空提示词并保存 → 服务端下次返回内置模板全文,回填到输入框。
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig({ ...cfg, prompt: "" });
      const j = await api.interceptGetJudgeConfig();
      setCfg(j);
      toast.success("已恢复内置默认模板");
    } catch (e) {
      toast.error("恢复失败: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-4">
      {/* 启用开关 —— 独立高亮条 */}
      <div
        className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-3 ${
          cfg.enabled ? "border-violet-400/50 bg-violet-50/40 dark:bg-violet-950/20" : "bg-muted/40"
        }`}
      >
        <div className="flex items-center gap-2.5">
          <BotIcon className={`h-5 w-5 shrink-0 ${cfg.enabled ? "text-violet-600" : "text-muted-foreground"}`} />
          <div>
            <p className="text-sm font-semibold leading-tight">模型兜底审批</p>
            <p className="text-xs text-muted-foreground mt-0.5">
              在<span className="font-medium text-foreground">拦截范围</span>内、且<span className="font-medium text-foreground">没有任何拦截规则命中</span>的命令，才由模型做语义判断（放行 / 转人工 / 拦截）
            </p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span className="text-xs text-muted-foreground">{cfg.enabled ? "已启用" : "未启用"}</span>
          <Switch checked={cfg.enabled} disabled={loading} onCheckedChange={(v) => patch({ enabled: v })} />
        </div>
      </div>

      {/* 审批 Token 用量统计(全局累计,独立于各模型配置) */}
      {cfg.enabled && usage && (
        <Card>
          <CardContent className="p-4">
            <div className="mb-3 flex items-center justify-between">
              <div>
                <p className="text-sm font-medium">审批 Token 消耗</p>
                <p className="text-xs text-muted-foreground">
                  模型兜底审批累计用量,单独计量(worker=judge),不与各模型配置的统计混在一起
                </p>
              </div>
              <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={loadUsage}>
                刷新
              </Button>
            </div>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
              <JudgeStat label="审批调用" value={usage.calls.toLocaleString()} />
              <JudgeStat label="输入 Token" value={fmtTokens(usage.input_tokens)} />
              <JudgeStat label="输出 Token" value={fmtTokens(usage.output_tokens)} />
              <JudgeStat label="缓存读" value={fmtTokens(usage.cache_read_tokens)} />
              <JudgeStat label="缓存写" value={fmtTokens(usage.cache_write_tokens)} />
            </div>
            {usage.daily.length > 0 && (
              <div className="mt-4">
                <p className="mb-2 text-[10px] uppercase tracking-wider text-muted-foreground">
                  近 30 天每日消耗(输入 + 输出)
                </p>
                <JudgeSparkbars daily={usage.daily} />
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {cfg.enabled && (
        <div className="grid gap-4 lg:grid-cols-5">
          {/* 左:提示词编辑器(直接展开,主区域) */}
          <Card className="lg:col-span-3">
            <CardContent className="flex h-full flex-col gap-2 p-4">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">审批提示词</p>
                  <p className="text-xs text-muted-foreground">模型据此判定 ALLOW / ASK / DENY，可直接编辑</p>
                </div>
                <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={restorePrompt} disabled={saving}>
                  恢复默认模板
                </Button>
              </div>
              <Textarea
                className="min-h-[22rem] flex-1 resize-none font-mono text-xs leading-relaxed"
                value={cfg.prompt}
                onChange={(e) => patch({ prompt: e.target.value })}
                placeholder="留空使用内置模板"
                spellCheck={false}
              />
              <p className="text-right text-[11px] text-muted-foreground">{cfg.prompt.length} 字</p>
            </CardContent>
          </Card>

          {/* 右:判定参数(设置栏) */}
          <Card className="lg:col-span-2">
            <CardContent className="space-y-5 p-4">
              <div className="space-y-4">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">判定模型与策略</p>
                <Field className="gap-1.5">
                  <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">审批模型</FieldLabel>
                  <Select value={String(cfg.profile_id || 0)} onValueChange={(v) => patch({ profile_id: Number(v) })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={FOLLOW_ACTIVE}>跟随激活配置</SelectItem>
                      {profiles.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}（{p.model}）
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field className="gap-1.5">
                  <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">模型判定超时（秒）</FieldLabel>
                  <Input
                    type="number"
                    min={1}
                    value={cfg.timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field className="gap-1.5">
                  <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">模型失败时（出错 / 超时 / 无法解析）</FieldLabel>
                  <Select value={cfg.fail_action} onValueChange={(v) => patch({ fail_action: v as JudgeConfig["fail_action"] })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="allow">放行</SelectItem>
                      <SelectItem value="ask">转人工审批</SelectItem>
                      <SelectItem value="deny">拦截</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>

              <Separator />

              <div className="space-y-4">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">人工审批（模型判为「转人工」时）</p>
                <Field className="gap-1.5">
                  <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">审批等待超时（秒）</FieldLabel>
                  <Input
                    type="number"
                    min={5}
                    value={cfg.ask_timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ ask_timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field className="gap-1.5">
                  <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">超时后默认动作</FieldLabel>
                  <Select value={cfg.ask_timeout_action} onValueChange={(v) => patch({ ask_timeout_action: v as JudgeConfig["ask_timeout_action"] })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="deny">拦截</SelectItem>
                      <SelectItem value="allow">放行</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      <div className="flex justify-end">
        <Button size="sm" onClick={save} disabled={saving || loading}>
          {saving ? "保存中…" : "保存配置"}
        </Button>
      </div>
    </div>
  );
}

// ---- page ----

export default function InterceptPage() {
  const [rules, setRules]     = React.useState<InterceptRule[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [open, setOpen]       = React.useState(false);
  const [editing, setEditing] = React.useState<InterceptRule | null>(null);
  const [form, setForm]       = React.useState<RuleForm>(defaultForm());
  const [saving, setSaving]   = React.useState(false);
  const [regexErr, setRegexErr] = React.useState("");
  const [regexWarn, setRegexWarn] = React.useState(false); // true = JS 无法解析但可能是合法 Go 语法

  // ---- 标签筛选 + 标签管理 ----
  const [knownTags, setKnownTags]       = React.useState<string[]>([]);
  const [agents, setAgents]             = React.useState<Agent[]>([]);
  const [tagFilter, setTagFilter]       = React.useState<string>("all"); // all | __none__ | 标签名
  const [tagManageOpen, setTagManageOpen] = React.useState(false);
  const [newTag, setNewTag]             = React.useState("");
  const [renamingTag, setRenamingTag]   = React.useState(""); // 正在重命名的标签名
  const [renameValue, setRenameValue]   = React.useState("");
  const [agentTag, setAgentTag]         = React.useState(""); // 正在批量设置 agent 的标签名
  const [agentPick, setAgentPick]       = React.useState<string[]>(["all"]);
  const [formTag, setFormTag]           = React.useState(""); // 编辑器内新建标签输入

  // ---- 规则多选 + 批量设置标签 ----
  const [selected, setSelected]         = React.useState<Set<number>>(new Set());
  const [batchOpen, setBatchOpen]       = React.useState(false);
  const [batchTags, setBatchTags]       = React.useState<string[]>([]); // 批量对话框内选中的标签
  const [batchSaving, setBatchSaving]   = React.useState(false);

  // ---- tool scope dialog ----
  const [scopeOpen, setScopeOpen]       = React.useState(false);
  const [allTools, setAllTools]         = React.useState<Tool[]>([]);
  const [enabledTools, setEnabledTools] = React.useState<Set<string>>(new Set());
  const [scopeLoading, setScopeLoading] = React.useState(false);
  const [scopeSaving, setScopeSaving]   = React.useState(false);
  const [scopeTools, setScopeTools]     = React.useState<string[]>([]); // 页头信息条:当前进入拦截的工具

  // ---- data ----

  const loadScope = React.useCallback(async () => {
    try {
      const cfg = await api.interceptGetToolConfig();
      setScopeTools(cfg.enabled_tools);
    } catch {
      // 信息条非关键,失败静默
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      const r = await api.interceptRules();
      setRules(r);
    } catch {
      toast.error("加载拦截规则失败");
    } finally {
      setLoading(false);
    }
  }, []);

  // 标签与 agent 列表:失败静默(不影响规则主列表)。
  const loadMeta = React.useCallback(async () => {
    try {
      setKnownTags(await api.interceptListTags());
    } catch { /* 非关键 */ }
    try {
      setAgents(await api.agents());
    } catch { /* 非关键 */ }
  }, []);

  React.useEffect(() => { load(); loadScope(); loadMeta(); }, [load, loadScope, loadMeta]);

  React.useEffect(() => {
    if (form.match_type !== "regex" || !form.pattern) { setRegexErr(""); setRegexWarn(false); return; }
    try {
      new RegExp(form.pattern);
      setRegexErr("");
      setRegexWarn(false);
    } catch {
      // JS RegExp 不支持 Go RE2 扩展语法（如 (?i) 内联 flag）。
      // 这里只是预览校验失败，不代表 Go 端无效；交给服务端最终验证。
      setRegexErr("");
      setRegexWarn(true);
    }
  }, [form.pattern, form.match_type]);

  // ---- rule handlers ----

  function set(patch: Partial<RuleForm>) { setForm(f => ({ ...f, ...patch })); }

  // refreshAgents 打开编辑器前刷新 agent 列表:新添加的自定义 agent 角色立即可选。
  function refreshAgents() {
    api.agents().then(setAgents).catch(() => { /* 保留旧列表 */ });
  }

  function openNew() {
    setEditing(null);
    setForm(defaultForm());
    setRegexErr("");
    refreshAgents();
    setOpen(true);
  }

  function openEdit(rule: InterceptRule) {
    setEditing(rule);
    setForm({
      name: rule.name, enabled: rule.enabled, priority: rule.priority,
      match_target: rule.match_target, match_type: rule.match_type,
      pattern: rule.pattern, action: rule.action, message: rule.message,
      timeout_enabled: rule.timeout_enabled, timeout_seconds: rule.timeout_seconds,
      timeout_action: rule.timeout_action,
      tags: rule.tags ?? [],
      agents: rule.agents?.length ? rule.agents : ["all"],
    });
    setRegexErr("");
    refreshAgents();
    setOpen(true);
  }

  // ---- 标签管理 ----

  // addKnownTag 新建全局标签(立即持久化到 settings),返回是否成功。
  async function addKnownTag(name: string): Promise<boolean> {
    const t = name.trim();
    if (!t) return false;
    if (knownTags.includes(t)) return true; // 已存在视为成功
    if (t.length > 32) { toast.error("标签长度不能超过 32 字符"); return false; }
    try {
      setKnownTags(await api.interceptSaveTags([...knownTags, t]));
      return true;
    } catch (e) {
      toast.error("新建标签失败: " + (e as Error).message);
      return false;
    }
  }

  async function handleAddTag() {
    if (await addKnownTag(newTag)) setNewTag("");
  }

  async function handleDeleteTag(name: string) {
    try {
      await api.interceptDeleteTag(name);
      toast.success(`标签「${name}」已删除`);
      if (tagFilter === name) setTagFilter("all");
      loadMeta();
      load(); // 规则上的标签引用已被服务端清除
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  // 标签重命名:settings 与所有规则上的引用同步替换。
  async function handleRenameTag(oldName: string) {
    const newName = renameValue.trim();
    if (!newName || newName === oldName) { setRenamingTag(""); return; }
    try {
      await api.interceptRenameTag(oldName, newName);
      toast.success(`标签已重命名为「${newName}」`);
      if (tagFilter === oldName) setTagFilter(newName);
      setRenamingTag("");
      setRenameValue("");
      loadMeta();
      load(); // 规则上的标签引用已被服务端替换
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  // 标签批量操作:把该标签下所有规则的适用 agent 覆盖为 agentPick。
  async function handleApplyAgents(tag: string) {
    try {
      const n = await api.interceptApplyAgentsForTag(tag, agentPick);
      toast.success(`已把「${tag}」下 ${n} 条规则的适用 agent 更新为 ${agentPick.includes("all") ? "全部" : agentPick.join("、")}`);
      setAgentTag("");
      load(); // 规则行的适用摘要随列表刷新
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  // 批量操作:把该标签追加到所有未分类规则。
  async function handleTagUntagged(tag: string) {
    try {
      const n = await api.interceptTagUntagged(tag);
      toast.success(n > 0 ? `已给 ${n} 条未分类规则加上「${tag}」` : "当前没有未分类规则");
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  // 编辑器内:切换规则标签选中态。
  function toggleFormTag(t: string) {
    setForm((f) => ({ ...f, tags: f.tags.includes(t) ? f.tags.filter((x) => x !== t) : [...f.tags, t] }));
  }

  // 编辑器内:新建标签并选中到当前规则。
  async function handleFormAddTag() {
    const t = formTag.trim();
    if (!t) return;
    if (await addKnownTag(t)) {
      if (!form.tags.includes(t)) toggleFormTag(t);
      setFormTag("");
    }
  }

  // 编辑器内:切换适用 agent。"all" 与具体 agent 互斥。
  function toggleFormAgent(key: string) {
    setForm((f) => {
      if (key === "all") return { ...f, agents: ["all"] };
      const cur = f.agents.filter((a) => a !== "all");
      const next = cur.includes(key) ? cur.filter((a) => a !== key) : [...cur, key];
      return { ...f, agents: next.length ? next : ["all"] };
    });
  }

  async function handleSave() {
    if (!form.name.trim())    { toast.error("名称不能为空"); return; }
    if (!form.pattern.trim()) { toast.error("模式不能为空"); return; }
    if (regexErr)             { toast.error("正则表达式语法无效"); return; }
    setSaving(true);
    try {
      if (editing) {
        await api.updateInterceptRule(editing.id, form);
        toast.success("规则已更新");
      } else {
        await api.createInterceptRule(form);
        toast.success("规则已创建");
      }
      setOpen(false);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete(id: number) {
    try {
      await api.deleteInterceptRule(id);
      toast.success("规则已删除");
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function handleToggle(rule: InterceptRule) {
    try {
      await api.toggleInterceptRule(rule.id, !rule.enabled);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  // ---- scope handlers ----

  async function openScope() {
    setScopeOpen(true);
    setScopeLoading(true);
    try {
      const [tools, cfg] = await Promise.all([api.tools(), api.interceptGetToolConfig()]);
      setAllTools(tools);
      setEnabledTools(new Set(cfg.enabled_tools));
    } catch (e) {
      toast.error("加载失败: " + (e as Error).message);
    } finally {
      setScopeLoading(false);
    }
  }

  function toggleTool(key: string, val: boolean) {
    setEnabledTools(prev => {
      const next = new Set(prev);
      if (val) next.add(key); else next.delete(key);
      return next;
    });
  }

  async function saveScope() {
    setScopeSaving(true);
    try {
      await api.interceptSetToolConfig([...enabledTools]);
      toast.success("拦截范围已保存");
      setScopeTools([...enabledTools]);
      setScopeOpen(false);
    } catch (e) {
      toast.error("保存失败: " + (e as Error).message);
    } finally {
      setScopeSaving(false);
    }
  }

  const toolGroups = React.useMemo(() => groupTools(allTools), [allTools]);

  // ---- 标签筛选 ----
  const visibleRules = React.useMemo(() => {
    if (tagFilter === "all") return rules;
    if (tagFilter === "__none__") return rules.filter((r) => (r.tags ?? []).length === 0);
    return rules.filter((r) => (r.tags ?? []).includes(tagFilter));
  }, [rules, tagFilter]);
  // 重新加载后剔除已不存在的选择 id。
  React.useEffect(() => {
    setSelected((prev) => {
      const alive = new Set(rules.map((r) => r.id));
      const next = new Set([...prev].filter((id) => alive.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [rules]);

  // ---- 多选/批量设置标签 ----

  function toggleSelectOne(id: number, on: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id); else next.delete(id);
      return next;
    });
  }

  function toggleSelectAll(on: boolean) {
    setSelected(on ? new Set(visibleRules.map((r) => r.id)) : new Set());
  }

  function openBatchDialog() {
    setBatchTags([]);
    setBatchOpen(true);
  }

  function toggleBatchTag(t: string) {
    setBatchTags((prev) => (prev.includes(t) ? prev.filter((x) => x !== t) : [...prev, t]));
  }

  async function handleBatchSetTags(mode: "append" | "replace") {
    setBatchSaving(true);
    try {
      const n = await api.interceptBatchSetTags([...selected], batchTags, mode);
      toast.success(`已更新 ${n} 条规则的标签`);
      setBatchOpen(false);
      setSelected(new Set());
      load(); // 规则行的标签徽标随列表刷新
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBatchSaving(false);
    }
  }

  const untaggedCount = React.useMemo(() => rules.filter((r) => (r.tags ?? []).length === 0).length, [rules]);
  const tagCount = React.useCallback(
    (t: string) => rules.filter((r) => (r.tags ?? []).includes(t)).length,
    [rules],
  );

  // chip 样式:小胶囊,选中反色。
  function Chip({ id, label, count }: { id: string; label: string; count?: number }) {
    const active = tagFilter === id;
    return (
      <button
        type="button"
        onClick={() => setTagFilter(id)}
        className={`inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs transition-colors ${
          active
            ? "border-foreground bg-foreground text-background"
            : "border-border bg-card text-muted-foreground hover:text-foreground"
        }`}
      >
        {label}
        {count !== undefined && <span className={active ? "opacity-70" : "opacity-60"}>{count}</span>}
      </button>
    );
  }

  // ---- render ----

  return (
    <div className="flex flex-1 flex-col gap-5 p-6">

      {/* ---- header ---- */}
      <div className="flex items-center gap-2.5">
        <ShieldAlertIcon className="h-5 w-5 shrink-0" />
        <div>
          <h1 className="text-lg font-semibold leading-tight">命令拦截</h1>
          <p className="text-sm text-muted-foreground mt-0.5">
            工具执行前先按拦截规则匹配；未命中的命令可交由模型兜底判定
          </p>
        </div>
      </div>

      {/* ---- 拦截范围信息条（规则匹配与模型兜底共用：不在范围内的工具两者都不介入）---- */}
      <div
        className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-2.5 ${
          scopeTools.length === 0
            ? "border-amber-400/60 bg-amber-50/50 dark:bg-amber-950/20"
            : "bg-muted/40"
        }`}
      >
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <ListFilterIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
          <span className="shrink-0 font-medium">拦截范围</span>
          {scopeTools.length === 0 ? (
            <span className="text-amber-700 dark:text-amber-500">
              未启用任何工具 — 拦截规则与模型兜底均不会生效
            </span>
          ) : (
            <>
              <Badge variant="secondary" className="shrink-0">{scopeTools.length} 个工具</Badge>
              <span className="truncate text-muted-foreground" title={scopeTools.join("、")}>
                {scopeTools.join("、")}
              </span>
            </>
          )}
        </div>
        <Button
          variant={scopeTools.length === 0 ? "default" : "outline"}
          size="sm"
          className="shrink-0"
          onClick={openScope}
        >
          <ListFilterIcon className="h-4 w-4" />
          调整范围
        </Button>
      </div>

      <Tabs defaultValue="rules" className="flex-1">
        <TabsList>
          <TabsTrigger value="rules">拦截规则</TabsTrigger>
          <TabsTrigger value="judge">模型配置</TabsTrigger>
        </TabsList>

        {/* ---- tab: 拦截规则 ---- */}
        <TabsContent value="rules" className="mt-4 flex flex-col gap-4">
          {/* 标签筛选 chips(左)+ 操作按钮(右)。新建规则保持在最上方操作区。 */}
          <div className="flex flex-wrap items-center gap-2">
            <Chip id="all" label="全部" />
            <Chip id="__none__" label="未分类" count={untaggedCount} />
            {knownTags.map((t) => (
              <Chip key={t} id={t} label={t} count={tagCount(t)} />
            ))}
            <button
              type="button"
              onClick={() => setTagManageOpen(true)}
              className="inline-flex items-center gap-1 rounded-full border border-dashed border-border px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              <TagIcon className="h-3 w-3" />
              标签管理
            </button>
            <span className="flex-1" />
            <p className="hidden text-xs text-muted-foreground lg:block">
              按优先级（数字越大越先）逐条匹配，首条命中的规则生效
            </p>
            <Button onClick={openNew} size="sm" className="shrink-0">
              <PlusIcon className="h-4 w-4" />
              新建规则
            </Button>
          </div>

          {/* 批量操作条:有选中规则时出现(表格上方,简洁一行) */}
          {selected.size > 0 && (
            <div className="flex items-center gap-2 rounded-lg border bg-muted/40 px-3 py-2">
              <span className="text-sm">已选 {selected.size} 条</span>
              <Button size="sm" variant="outline" className="h-7" onClick={openBatchDialog}>
                <TagIcon className="h-3.5 w-3.5" />
                批量设置标签
              </Button>
              <Button size="sm" variant="ghost" className="h-7" onClick={() => setSelected(new Set())}>
                清除选择
              </Button>
            </div>
          )}

          <Card>
            <CardContent className="p-0">
          {loading ? (
            <p className="p-6 text-sm text-muted-foreground">加载中…</p>
          ) : visibleRules.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 py-16 text-center">
              <ShieldAlertIcon className="h-8 w-8 text-muted-foreground/40" />
              <p className="text-sm text-muted-foreground">
                {tagFilter === "all" ? "暂无规则" : tagFilter === "__none__" ? "没有未分类规则" : `标签「${tagFilter}」下暂无规则`}
              </p>
              <Button size="sm" variant="outline" onClick={openNew}>
                <PlusIcon className="h-4 w-4" />
                新建{tagFilter === "all" || tagFilter === "__none__" ? "第一条" : "该标签下的"}规则
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-[36px]">
                    <Checkbox
                      checked={visibleRules.length > 0 && visibleRules.every((r) => selected.has(r.id))}
                      onCheckedChange={(v) => toggleSelectAll(Boolean(v))}
                      aria-label="全选当前可见规则"
                    />
                  </TableHead>
                  <TableHead className="w-[72px]">优先级</TableHead>
                  <TableHead>名称</TableHead>
                  <TableHead className="w-[90px]">目标</TableHead>
                  <TableHead className="w-[80px]">类型</TableHead>
                  <TableHead>模式</TableHead>
                  <TableHead className="w-[72px]">策略</TableHead>
                  <TableHead className="w-[64px] text-center">启用</TableHead>
                  <TableHead className="w-[80px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleRules.map((rule) => (
                  <TableRow key={rule.id} className={!rule.enabled ? "opacity-40" : ""}>
                    <TableCell>
                      <Checkbox
                        checked={selected.has(rule.id)}
                        onCheckedChange={(v) => toggleSelectOne(rule.id, Boolean(v))}
                        aria-label={`选择规则 ${rule.name}`}
                      />
                    </TableCell>
                    <TableCell>
                      <span className="font-mono text-xs tabular-nums">{rule.priority}</span>
                    </TableCell>
                    <TableCell className="font-medium text-sm">
                      <div>{rule.name}</div>
                      {/* 标签 + 适用 agent 摘要(名称下方的次级信息行) */}
                      {((rule.tags ?? []).length > 0 || (rule.agents ?? []).length > 0) && (
                        <div className="mt-1 flex flex-wrap items-center gap-1">
                          {(rule.tags ?? []).map((t) => (
                            <Badge key={t} variant="outline" className="px-1.5 py-0 text-[10px] font-normal">
                              {t}
                            </Badge>
                          ))}
                          <span className="text-[10px] text-muted-foreground">
                            {(rule.agents ?? []).includes("all")
                              ? "全部 agent"
                              : `适用:${(rule.agents ?? []).map((a) => agents.find((x) => x.key === a)?.name ?? a).join("、")}`}
                          </span>
                        </div>
                      )}
                    </TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {rule.match_target === "tool_name" ? "工具名" : "输入内容"}
                      </span>
                    </TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {rule.match_type === "regex" ? "正则" : "字符串"}
                      </span>
                    </TableCell>
                    <TableCell className="max-w-[220px]">
                      <code className="block truncate rounded bg-muted px-1.5 py-0.5 text-xs font-mono">
                        {rule.pattern}
                      </code>
                    </TableCell>
                    <TableCell>
                      <ActionBadge action={rule.action} />
                    </TableCell>
                    <TableCell className="text-center">
                      <Switch
                        checked={rule.enabled}
                        onCheckedChange={() => handleToggle(rule)}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center justify-end gap-0.5">
                        <Button
                          size="icon" variant="ghost" className="h-7 w-7"
                          onClick={() => openEdit(rule)}
                        >
                          <PencilIcon className="h-3.5 w-3.5" />
                        </Button>
                        <Button
                          size="icon" variant="ghost"
                          className="h-7 w-7 text-destructive hover:text-destructive"
                          onClick={() => handleDelete(rule.id)}
                        >
                          <Trash2Icon className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
            </CardContent>
          </Card>
        </TabsContent>

        {/* ---- tab: 模型配置 ---- */}
        <TabsContent value="judge" className="mt-4">
          <JudgeCard />
        </TabsContent>
      </Tabs>

      {/* ---- editor sheet ---- */}
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="flex flex-col gap-0 p-0 sm:max-w-md">
          <SheetHeader className="border-b px-6 py-4">
            <SheetTitle>{editing ? "编辑规则" : "新建规则"}</SheetTitle>
            <SheetDescription className="text-xs">
              优先级越大越先匹配；首条命中规则生效，后续跳过
            </SheetDescription>
          </SheetHeader>

          <div className="flex-1 min-h-0 overflow-y-auto px-6 py-5 space-y-5">
            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">名称</FieldLabel>
              <Input
                placeholder="给规则起个名字"
                value={form.name}
                onChange={(e) => set({ name: e.target.value })}
              />
            </Field>

            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">优先级（数字越大越先匹配）</FieldLabel>
              <Input
                type="number"
                value={form.priority}
                onChange={(e) => set({ priority: parseInt(e.target.value) || 0 })}
              />
            </Field>

            <Separator />

            {/* 标签:从已知标签多选,或内联新建 */}
            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">标签（用于列表分组筛选）</FieldLabel>
              <div className="flex flex-wrap items-center gap-1.5">
                {knownTags.length === 0 && (
                  <span className="text-xs text-muted-foreground">暂无标签,可在下方输入新建</span>
                )}
                {knownTags.map((t) => {
                  const on = form.tags.includes(t);
                  return (
                    <button
                      key={t}
                      type="button"
                      onClick={() => toggleFormTag(t)}
                      className={`rounded-full border px-2.5 py-1 text-xs transition-colors ${
                        on
                          ? "border-foreground bg-foreground text-background"
                          : "border-border bg-card text-muted-foreground hover:text-foreground"
                      }`}
                    >
                      {t}
                    </button>
                  );
                })}
              </div>
              <div className="flex items-center gap-2">
                <Input
                  placeholder="新建标签…"
                  value={formTag}
                  onChange={(e) => setFormTag(e.target.value)}
                  onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); handleFormAddTag(); } }}
                  className="h-8 text-xs"
                />
                <Button type="button" variant="outline" size="sm" className="h-8 shrink-0" onClick={handleFormAddTag}>
                  添加
                </Button>
              </div>
            </Field>

            {/* 适用 agent:all 或具体 agent 多选 */}
            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">适用 agent</FieldLabel>
              <div className="flex flex-wrap items-center gap-1.5">
                <button
                  type="button"
                  onClick={() => toggleFormAgent("all")}
                  className={`rounded-full border px-2.5 py-1 text-xs transition-colors ${
                    form.agents.includes("all")
                      ? "border-foreground bg-foreground text-background"
                      : "border-border bg-card text-muted-foreground hover:text-foreground"
                  }`}
                >
                  全部
                </button>
                {agents.map((a) => {
                  const on = !form.agents.includes("all") && form.agents.includes(a.key);
                  return (
                    <button
                      key={a.key}
                      type="button"
                      onClick={() => toggleFormAgent(a.key)}
                      className={`rounded-full border px-2.5 py-1 text-xs transition-colors ${
                        on
                          ? "border-foreground bg-foreground text-background"
                          : "border-border bg-card text-muted-foreground hover:text-foreground"
                      }`}
                    >
                      {a.name}
                      <span className="opacity-60">({a.key})</span>
                    </button>
                  );
                })}
              </div>
              <p className="text-[11px] text-muted-foreground">
                「全部」对所有 agent 生效;选择具体 agent 后规则只对被选中的 agent 生效
              </p>
            </Field>

            <Separator />

            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">匹配目标</FieldLabel>
              <Select
                value={form.match_target}
                onValueChange={(v) => set({ match_target: v as RuleForm["match_target"] })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="tool_name">工具名（tool_name）</SelectItem>
                  <SelectItem value="tool_input">输入内容（tool_input JSON）</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">匹配类型</FieldLabel>
              <Select
                value={form.match_type}
                onValueChange={(v) => set({ match_type: v as RuleForm["match_type"] })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="string">字符串包含</SelectItem>
                  <SelectItem value="regex">正则表达式</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">模式</FieldLabel>
              <Input
                placeholder={form.match_type === "regex" ? "^Bash$" : "rm -rf"}
                value={form.pattern}
                onChange={(e) => set({ pattern: e.target.value })}
                className={regexErr ? "border-destructive focus-visible:ring-destructive" : ""}
              />
              {regexErr && (
                <p className="text-xs text-destructive mt-1">{regexErr}</p>
              )}
              {regexWarn && (
                <p className="text-xs text-amber-600 mt-1">包含 Go RE2 扩展语法（如 <code className="font-mono">(?i)</code>），浏览器无法预览，提交后由服务端验证</p>
              )}
            </Field>

            <Separator />

            <Field className="gap-1.5">
              <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">拦截策略</FieldLabel>
              <Select
                value={form.action}
                onValueChange={(v) => set({ action: v as InterceptAction })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="allow">允许 — 直接放行，跳过后续规则</SelectItem>
                  <SelectItem value="deny">禁止 — 阻断，返回拒绝消息给模型</SelectItem>
                  <SelectItem value="ask">向用户申请 — 等待审批</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            {form.action !== "allow" && (
              <Field className="gap-1.5">
                <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
                  {form.action === "deny" ? "拒绝消息（返回给模型）" : "审批说明（可选）"}
                </FieldLabel>
                <Textarea
                  placeholder={form.action === "deny" ? "操作被安全策略阻止" : ""}
                  value={form.message}
                  onChange={(e) => set({ message: e.target.value })}
                  rows={2}
                  className="resize-none"
                />
              </Field>
            )}

            {form.action === "ask" && (
              <>
                <Separator />
                <div className="flex items-center justify-between">
                  <div>
                    <p className="text-sm font-medium">启用审批超时</p>
                    <p className="text-xs text-muted-foreground">超时后自动处置，不再等待</p>
                  </div>
                  <Switch
                    checked={form.timeout_enabled}
                    onCheckedChange={(v) => set({ timeout_enabled: v })}
                  />
                </div>
                {form.timeout_enabled && (
                  <div className="flex items-end gap-3">
                    <Field className="gap-1.5">
                      <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">超时时间（秒）</FieldLabel>
                      <Input
                        type="number"
                        min={5}
                        className="w-28"
                        value={form.timeout_seconds}
                        onChange={(e) => {
                          const n = parseInt(e.target.value, 10);
                          if (n > 0) set({ timeout_seconds: n });
                        }}
                      />
                    </Field>
                    <Field className="gap-1.5">
                      <FieldLabel className="text-xs font-medium text-muted-foreground uppercase tracking-wide">超时动作</FieldLabel>
                      <Select
                        value={form.timeout_action}
                        onValueChange={(v) => set({ timeout_action: v as "deny" | "allow" })}
                      >
                        <SelectTrigger className="w-32"><SelectValue /></SelectTrigger>
                        <SelectContent>
                          <SelectItem value="deny">自动拒绝</SelectItem>
                          <SelectItem value="allow">自动允许</SelectItem>
                        </SelectContent>
                      </Select>
                    </Field>
                  </div>
                )}
              </>
            )}

            <Separator />

            <div className="flex items-center gap-3">
              <Switch
                id="rule-enabled"
                checked={form.enabled}
                onCheckedChange={(v) => set({ enabled: v })}
              />
              <Label htmlFor="rule-enabled" className="cursor-pointer">启用此规则</Label>
            </div>
          </div>

          <SheetFooter className="border-t px-6 py-4 flex-row justify-end gap-2">
            <Button variant="outline" onClick={() => setOpen(false)}>取消</Button>
            <Button onClick={handleSave} disabled={saving || !!regexErr}>
              {saving ? "保存中…" : "保存"}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      {/* ---- tag manage dialog ---- */}
      <Dialog open={tagManageOpen} onOpenChange={setTagManageOpen}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>标签管理</DialogTitle>
            <DialogDescription className="text-xs">
              标签用于拦截规则的分组筛选;删除标签会同时从所有规则上移除
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2">
            <Input
              placeholder="新标签名称…"
              value={newTag}
              onChange={(e) => setNewTag(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); handleAddTag(); } }}
            />
            <Button size="sm" className="shrink-0" onClick={handleAddTag}>
              <PlusIcon className="h-4 w-4" />
              添加
            </Button>
          </div>
          <div className="mt-2 flex max-h-64 flex-col gap-1 overflow-y-auto">
            {knownTags.length === 0 ? (
              <p className="py-6 text-center text-xs text-muted-foreground">暂无标签</p>
            ) : (
              knownTags.map((t) => (
                <div key={t} className="rounded-md border px-3 py-1.5">
                <div className="flex items-center gap-2">
                  <TagIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  {renamingTag === t ? (
                    <>
                      <Input
                        autoFocus
                        value={renameValue}
                        onChange={(e) => setRenameValue(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") { e.preventDefault(); handleRenameTag(t); }
                          if (e.key === "Escape") setRenamingTag("");
                        }}
                        className="h-7 text-sm"
                      />
                      <span className="flex-1" />
                      <Button size="sm" variant="outline" className="h-7" onClick={() => handleRenameTag(t)}>
                        确定
                      </Button>
                      <Button size="sm" variant="ghost" className="h-7" onClick={() => setRenamingTag("")}>
                        取消
                      </Button>
                    </>
                  ) : (
                    <>
                      <span className="shrink-0 whitespace-nowrap text-sm">{t}</span>
                      <span className="shrink-0 whitespace-nowrap text-xs text-muted-foreground">{tagCount(t)} 条规则</span>
                      <span className="flex-1" />
                      {untaggedCount > 0 && (
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 px-2 text-xs"
                          title={`把 ${untaggedCount} 条未分类规则归入「${t}」`}
                          onClick={() => handleTagUntagged(t)}
                        >
                          收纳未分类({untaggedCount})
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="outline"
                        className="h-7 px-2 text-xs"
                        onClick={() => { setAgentTag(agentTag === t ? "" : t); setAgentPick(["all"]); }}
                      >
                        适用 agent
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 px-2 text-xs"
                        onClick={() => { setRenamingTag(t); setRenameValue(t); }}
                      >
                        重命名
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 px-2 text-xs text-destructive hover:text-destructive"
                        onClick={() => handleDeleteTag(t)}
                      >
                        删除
                      </Button>
                    </>
                  )}
                </div>
                {/* 批量设置 agent 面板:选择后覆盖该标签下所有规则 */}
                {agentTag === t && (
                  <div className="mt-2 border-t pt-2">
                    <p className="mb-1.5 text-[11px] text-muted-foreground">
                      选择适用 agent,应用到「{t}」下全部 {tagCount(t)} 条规则:
                    </p>
                    <div className="flex flex-wrap items-center gap-1.5">
                      {["all", ...agents.map((a) => a.key)].map((key) => {
                        const on = agentPick.includes(key);
                        const label = key === "all" ? "全部" : (agents.find((a) => a.key === key)?.name ?? key);
                        return (
                          <button
                            key={key}
                            type="button"
                            onClick={() =>
                              setAgentPick((prev) => {
                                if (key === "all") return ["all"];
                                const cur = prev.filter((x) => x !== "all");
                                const next = cur.includes(key) ? cur.filter((x) => x !== key) : [...cur, key];
                                return next.length ? next : ["all"];
                              })
                            }
                            className={`rounded-full border px-2 py-0.5 text-[11px] transition-colors ${
                              on
                                ? "border-foreground bg-foreground text-background"
                                : "border-border bg-card text-muted-foreground hover:text-foreground"
                            }`}
                          >
                            {label}
                          </button>
                        );
                      })}
                    </div>
                    <div className="mt-2 flex justify-end gap-2">
                      <Button size="sm" variant="ghost" className="h-7" onClick={() => setAgentTag("")}>
                        取消
                      </Button>
                      <Button size="sm" className="h-7" onClick={() => handleApplyAgents(t)}>
                        应用到 {tagCount(t)} 条规则
                      </Button>
                    </div>
                  </div>
                )}
                </div>
              ))
            )}
          </div>
        </DialogContent>
      </Dialog>

      {/* ---- 批量设置标签 dialog ---- */}
      <Dialog open={batchOpen} onOpenChange={setBatchOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>批量设置标签</DialogTitle>
            <DialogDescription className="text-xs">
              为选中的 {selected.size} 条规则追加或覆盖标签
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-wrap items-center gap-1.5">
            {knownTags.length === 0 && (
              <span className="text-xs text-muted-foreground">暂无标签,请先在「标签管理」中新建</span>
            )}
            {knownTags.map((t) => {
              const on = batchTags.includes(t);
              return (
                <button
                  key={t}
                  type="button"
                  onClick={() => toggleBatchTag(t)}
                  className={`rounded-full border px-2.5 py-1 text-xs transition-colors ${
                    on
                      ? "border-foreground bg-foreground text-background"
                      : "border-border bg-card text-muted-foreground hover:text-foreground"
                  }`}
                >
                  {t}
                </button>
              );
            })}
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setBatchOpen(false)}>取消</Button>
            <Button
              size="sm"
              variant="outline"
              disabled={batchTags.length === 0 || batchSaving}
              onClick={() => handleBatchSetTags("append")}
            >
              追加到所选 {selected.size} 条
            </Button>
            <Button
              size="sm"
              disabled={batchTags.length === 0 || batchSaving}
              onClick={() => handleBatchSetTags("replace")}
            >
              覆盖所选 {selected.size} 条
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {/* ---- scope dialog ---- */}
      <Dialog open={scopeOpen} onOpenChange={setScopeOpen}>
        <DialogContent className="sm:max-w-lg flex flex-col overflow-hidden p-0 gap-0" style={{ maxHeight: "min(80vh, 560px)" }}>
          <DialogHeader className="shrink-0 border-b px-6 py-4">
            <DialogTitle className="flex items-center gap-2">
              <ListFilterIcon className="h-4 w-4" />
              拦截范围
            </DialogTitle>
            <DialogDescription className="text-xs">
              只有启用拦截的工具才会进入规则匹配；其余工具直接放行
            </DialogDescription>
          </DialogHeader>

          <div className="flex-1 min-h-0 overflow-y-auto px-6 py-4 space-y-5">
            {scopeLoading ? (
              <p className="text-sm text-muted-foreground py-4">加载中…</p>
            ) : (
              toolGroups.map((group, gi) => (
                <div key={group.label}>
                  {gi > 0 && <Separator className="mb-5" />}
                  <p className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                    {group.label}
                  </p>
                  <div className="space-y-0.5">
                    {group.tools.map((t) => (
                      <div key={t.key} className="flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-muted/50">
                        <Switch
                          id={`scope-${t.key}`}
                          checked={enabledTools.has(t.key)}
                          onCheckedChange={(v) => toggleTool(t.key, v)}
                        />
                        <label htmlFor={`scope-${t.key}`} className="flex-1 min-w-0 cursor-pointer">
                          <div className="flex items-center gap-1.5">
                            <span className="font-mono text-sm">{t.key}</span>
                            {(t.kind && t.kind !== "builtin") && (
                              <Badge variant="outline" className="px-1 py-0 text-[10px]">{t.kind}</Badge>
                            )}
                          </div>
                          {t.description && (
                            <p className="text-[11px] text-muted-foreground line-clamp-1">{t.description}</p>
                          )}
                        </label>
                      </div>
                    ))}
                  </div>
                </div>
              ))
            )}
          </div>

          <div className="shrink-0 border-t px-6 py-3 flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setScopeOpen(false)}>取消</Button>
            <Button size="sm" onClick={saveScope} disabled={scopeSaving || scopeLoading}>
              {scopeSaving ? "保存中…" : "保存"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
