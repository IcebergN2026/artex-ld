// ═══ ARTEX 二开 · [P-INTENT·U3 审核面板] ═══
// 功能介绍：ReviewPanel——审核会话完整面板：待审意图卡片区（置顶独立区）、review agent 消息流
//           （人工右气泡/agent 左气泡/系统事件中轴胶囊/错误红卡）、intercept ask 确认卡停靠 composer
//           上方、驳回理由对话框、自动处置 Switch（开=agent 自主裁决；关=人工全审）
// 二开日期：2026-10-21（2026-10-25 全面重设计 + 人工全审语义）
"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
	CheckIcon,
	XIcon,
	ArrowUpIcon,
	TriangleAlertIcon,
	Loader2Icon,
} from "lucide-react";
import { api } from "@/lib/api";
import type { InterceptPending, ReviewMessage } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupTextarea } from "@/components/ui/input-group";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
	AlertDialog,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alert-dialog";

// ── 类型 ──────────────────────────────────────────────

interface ProposedIntent {
	id: number;
	priority: number;
	summary: string;
	pending_executor: string | null; // m00973/K24：送审时的预选执行器（payload.pending_executor），approve 时后端自动转正
	replaces: number | null;
	review_note: string | null; // K20：AI 预审摘要（≤200 字）
	created_at: string;
	content_version?: number; // 审核流 PR3：批准/驳回时回传做 expected_version CAS
}

// 【二开·P-REVIEW·U3c】驳回理由草稿：默认以 AI 预审意见（payload.review_note）回填，
// 人可直接编辑——预审意见已经是“这条意图为什么要改/为什么不放行”的描述，用它做草稿比空白框好。
// 无预审意见时返回空串（保持原行为：必须人自己写）。
function prefillRejectReason(it: ProposedIntent | null): string {
	const note = (it?.review_note ?? "").trim();
	return note;
}



// 系统事件（预审落账/放行/驳回通知等）渲染为中轴胶囊，不占聊天气泡。
const SYS_RE = /^(预审|意图 #|【系统】)/;

function fmtTime(unixSec: number): string {
	const d = new Date(unixSec * 1000);
	return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString();
}

// ── 主组件 ────────────────────────────────────────────

export function ReviewPanel({ taskId }: { taskId: string }) {
	const [messages, setMessages] = useState<ReviewMessage[]>([]);
	const [proposed, setProposed] = useState<ProposedIntent[]>([]);
	const [pending, setPending] = useState<InterceptPending[]>([]);
	const [input, setInput] = useState("");
	const [sending, setSending] = useState(false);
	const [decided, setDecided] = useState<Record<number, "approved" | "rejected">>({});
	// 驳回对话框
	const [rejectTarget, setRejectTarget] = useState<ProposedIntent | null>(null);
	const [rejectReason, setRejectReason] = useState("");
	const bottomRef = useRef<HTMLDivElement>(null);
	// 自动跟滚：仅当用户本就贴着底部时才跟随新消息；翻上去看历史绝不拉回去。
	const scrollAreaRef = useRef<HTMLDivElement>(null);
	const stickBottomRef = useRef(true);
	// 「思考中」指示：发送后等待 agent 回话（轮询 3s 拿回）。>45s 显示连通性提示，>120s 放弃。
	const awaitSinceRef = useRef(0);
	const [awaiting, setAwaiting] = useState(false);
	const [awaitSlow, setAwaitSlow] = useState(false);

	useEffect(() => {
		const vp = scrollAreaRef.current?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]');
		if (!vp) return;
		const onScroll = () => {
			stickBottomRef.current = vp.scrollHeight - vp.scrollTop - vp.clientHeight < 60;
		};
		vp.addEventListener("scroll", onScroll, { passive: true });
		return () => vp.removeEventListener("scroll", onScroll);
	}, []);

	// 审核流 PR2.9.5：执行器在送审/修订时确定，批准时后端转正，面板不再提供下拉改选

	// 轮询：消息流（3s）+ 待审列表（5s）+ intercept 确认卡（2s）
	const loadMessages = useCallback(() => {
		api.reviewMessages(taskId)
			.then((list) => {
				setMessages(list);
				// 人工消息之后出现了新的非人工消息 → 回复已到，撤掉「思考中」。
				if (awaitSinceRef.current > 0 && list.some((m) => m.kind !== "user" && m.created_at * 1000 > awaitSinceRef.current)) {
					awaitSinceRef.current = 0;
					setAwaiting(false);
					setAwaitSlow(false);
				}
			})
			.catch(() => {});
	}, [taskId]);
	const loadProposed = useCallback(() => {
		// 审核端点列表查询带 executor='review' 双条件过滤（D3 ProposedIntents SQL，m00973/K24）
		api.reviewProposed(taskId).then((list) => setProposed(list as unknown as ProposedIntent[])).catch(() => {});
	}, [taskId]);
	const loadPending = useCallback(() => {
		// interceptTask 返回已解包的数组（api.ts:1258 .then((r) => arr(r.items))）
		api.interceptTask(taskId)
			.then((list) => setPending(list.filter((p) => p.status === "pending" && p.agent_name === "review")))
			.catch(() => {});
	}, [taskId]);

	useEffect(() => {
		loadMessages();
		loadProposed();
		loadPending();
		const t1 = setInterval(loadMessages, 3000);
		const t2 = setInterval(loadProposed, 5000);
		const t3 = setInterval(loadPending, 2000);
		return () => { clearInterval(t1); clearInterval(t2); clearInterval(t3); };
	}, [loadMessages, loadProposed, loadPending]);

	// 思考中超时降级：>45s 提示检查 LLM 连通性；>120s 撤掉指示（错误会以红卡形式出现在消息流）。
	useEffect(() => {
		if (!awaiting) return;
		const slowT = setTimeout(() => setAwaitSlow(true), 45000);
		const giveUpT = setTimeout(() => { awaitSinceRef.current = 0; setAwaiting(false); setAwaitSlow(false); }, 120000);
		return () => { clearTimeout(slowT); clearTimeout(giveUpT); };
	}, [awaiting]);

	useEffect(() => {
		if (stickBottomRef.current) bottomRef.current?.scrollIntoView({ block: "end" });
	}, [messages, proposed, awaiting]);

	// 处置动作
	const approve = async (it: ProposedIntent) => {
		try {
			// 审核流 PR3：content_version 回传做 expected_version CAS；executor 由后端从 pending_executor 转正
			await api.reviewApprove(taskId, it.id, it.content_version);
			setDecided((d) => ({ ...d, [it.id]: "approved" }));
			setProposed((l) => l.filter((x) => x.id !== it.id));
		} catch { /* 409=已被处理，下轮轮询自然消失 */ }
	};
	const doReject = async () => {
		if (!rejectTarget || !rejectReason.trim()) return;
		try {
			await api.reviewReject(taskId, rejectTarget.id, rejectReason.trim(), rejectTarget.content_version);
			setDecided((d) => ({ ...d, [rejectTarget.id]: "rejected" }));
			setProposed((l) => l.filter((x) => x.id !== rejectTarget.id));
		} catch { /* 409=意图已被其它通道处理，下轮轮询自然从列表消失 */ }
		setRejectTarget(null);
		setRejectReason("");
	};
	// 【二开·P-REVIEW·U3b】起 —— 自动处置 Switch（tasks.review_auto，PATCH 落库即生效，ReviewRunner 下一轮 60s 内接管）
	const [reviewAuto, setReviewAuto] = useState(false);
	const [autoToggling, setAutoToggling] = useState(false);
	// 人工全审：任务未开启审核时面板显示空态（tab 常驻，不再按开关隐藏）。
	const [taskReviewEnabled, setTaskReviewEnabled] = useState<boolean | null>(null);
	useEffect(() => {
		api.task(taskId).then((t) => { setReviewAuto(!!t.review_auto); setTaskReviewEnabled(!!t.review_enabled); }).catch(() => {});
	}, [taskId]);
	const toggleAuto = async (v: boolean) => {
		if (autoToggling) return;
		setAutoToggling(true);
		try {
			await api.updateTask(taskId, { review_auto: v });
			setReviewAuto(v);
		} catch { /* 失败保持原态 */ } finally {
			setAutoToggling(false);
		}
	};
	// 【二开·P-REVIEW·U3b】止

	const send = async () => {
		const v = input.trim();
		if (!v || sending) return;
		setInput("");
		setSending(true);
		stickBottomRef.current = true; // 自己发言后强制回到底部
		try {
			await api.reviewChat(taskId, v); // 202 accepted，回复走消息流轮询回来
			awaitSinceRef.current = Date.now();
			setAwaiting(true);
			setAwaitSlow(false);
		} finally {
			setSending(false);
			setTimeout(loadMessages, 800);
		}
	};
	const decidePending = async (p: InterceptPending, decision: "allowed" | "denied") => {
		try {
			await api.interceptDecide(p.id, decision);
			setPending((l) => l.filter((x) => x.id !== p.id));
		} catch { /* 已超时/被处理 */ }
	};

	// 未开启审核的任务：常驻空态（所有 hooks 已执行完毕，此处早退安全）。
	if (taskReviewEnabled === false) {
		return (
				<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
				<p>该任务未开启意图审核</p>
				<p className="text-xs opacity-70">开启后所有意图均待人工批准/驳回；打开「自动处置」才由审核 agent 自主裁决</p>
			</div>
		);
	}

	return (
		<div className="flex min-h-0 flex-1 flex-col">
			{/* ── 工具行：待审计数/模式说明 + 自动处置开关（与其他会话窗一致的素面风格，无图标） ── */}
			<div className="flex items-center justify-between gap-3 border-b px-4 py-2">
				<div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
					{proposed.length > 0 && (
						<Badge variant="outline" className="border-amber-300 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-400">
							待审 {proposed.length}
						</Badge>
					)}
					{reviewAuto && (
						<Badge variant="outline" className="border-emerald-300 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-400">
							自动审核中 · 每 60s 一轮
						</Badge>
					)}
				</div>
				<div className="flex flex-none items-center gap-2">
					<label htmlFor="review-auto" className="cursor-pointer text-xs font-medium text-muted-foreground">自动处置</label>
					<Switch checked={reviewAuto} disabled={autoToggling} onCheckedChange={toggleAuto} id="review-auto" />
				</div>
			</div>

			{/* ── 待审意图卡片区（置顶独立区；卡片用原版虚线琥珀风格） ── */}
			{proposed.length > 0 && (
				<div className="border-b">
					<div className="px-4 pt-2.5 text-[11px] font-medium text-amber-700 dark:text-amber-400">
						待审意图（{proposed.length}）
						<span className="ml-2 font-normal text-muted-foreground">批准后按预选执行器立即派发</span>
					</div>
					<div className="max-h-64 space-y-2 overflow-y-auto px-4 py-2.5">
						{proposed.map((it) => (
							<div key={it.id} className="rounded-lg border border-dashed border-amber-500 bg-amber-50 px-3.5 py-2.5 dark:bg-amber-950/20">
								<div className="flex items-center gap-2">
									<span className="font-mono text-sm font-bold text-blue-600 dark:text-blue-400">#{it.id}</span>
									<Badge className="bg-amber-600 text-white">pri={it.priority}</Badge>
									<Badge variant="destructive" className="animate-pulse">待审</Badge>
									<span className="ml-auto text-xs text-muted-foreground">
										执行器：<b className="text-foreground">{it.pending_executor || "默认 worker"}</b>
									</span>
									{it.replaces ? (
										<Badge variant="outline" className="border-blue-200 bg-blue-50 text-blue-600 dark:border-blue-900 dark:bg-blue-950/40 dark:text-blue-400" title={`替代旧意图 #${it.replaces}（旧意图 stopped，整行还在库里）`}>
											替代 #{it.replaces}
										</Badge>
									) : null}
								</div>
								<div className="pt-1.5 text-sm leading-6">{it.summary}</div>
								<div className="pb-1 pt-1.5">
									{it.review_note ? (
										<div className="rounded-r-md border-l-[3px] border-amber-500 bg-amber-100/70 px-2.5 py-1.5 text-xs leading-6 text-amber-800 dark:bg-amber-950/40 dark:text-amber-300">
											预审：{it.review_note}
										</div>
									) : (
										<div className="rounded-r-md border-l-[3px] border-zinc-300 bg-muted px-2.5 py-1.5 text-xs italic text-muted-foreground dark:border-zinc-700">
											预审中…（review agent 轮询后补充建议）
										</div>
									)}
								</div>
								<div className="mt-1 flex justify-end gap-2">
									<Button size="sm" variant="outline" className="border-red-200 text-red-600 hover:bg-red-50 dark:border-red-900 dark:hover:bg-red-950/40" onClick={() => { setRejectTarget(it); setRejectReason(prefillRejectReason(it)); }}>
										<XIcon className="mr-1 h-3.5 w-3.5" />驳回
									</Button>
									<Button size="sm" className="bg-green-600 hover:bg-green-700" onClick={() => approve(it)}>
										<CheckIcon className="mr-1 h-3.5 w-3.5" />通过
									</Button>
								</div>
							</div>
						))}
					</div>
				</div>
			)}

			{/* ── 消息流（ScrollArea 滚动；flex-1 配 min-h-0 视口才拿得到确定高度） ── */}
			<ScrollArea ref={scrollAreaRef} className="min-h-0 flex-1">
				<div className="flex flex-col gap-2.5 px-4 py-3">
					{messages.length === 0 && !awaiting && (
						<div className="py-10 text-center text-xs text-muted-foreground">
							暂无审核消息——可以直接问 review agent，例如「简要概括下当前待审行动计划」
						</div>
					)}
					{messages.map((m) => {
						if (m.kind === "user") {
							const text = (m.detail || m.summary).replace(/^【人工】/, "");
							return (
								<div key={m.id} className="flex max-w-[82%] flex-row-reverse items-end gap-2 self-end">
									<div className="whitespace-pre-wrap rounded-lg bg-blue-600 px-3 py-2 text-sm leading-6 text-white">
										{text}
										<span className="mt-1 block text-right text-[10px] text-blue-200">{fmtTime(m.created_at)}</span>
									</div>
								</div>
							);
						}
						if (m.is_error) {
							return (
								<div key={m.id} className="mx-auto flex max-w-[88%] items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs leading-5 text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-400">
									<TriangleAlertIcon className="mt-0.5 h-3.5 w-3.5 flex-none" />
									<span>{m.summary}<span className="ml-2 text-[10px] opacity-70">{fmtTime(m.created_at)}</span></span>
								</div>
							);
						}
						if (SYS_RE.test(m.summary)) {
							return (
								<div key={m.id} className="mx-auto max-w-[88%] rounded-full bg-muted px-3.5 py-1 text-center text-[11px] leading-5 text-muted-foreground">
									{m.summary}
								</div>
							);
						}
						return (
							<div key={m.id} className="flex max-w-[82%] items-start gap-2">
								<div className="whitespace-pre-wrap rounded-lg border bg-card px-3 py-2 text-sm leading-6">
									{m.detail || m.summary}
									<span className="mt-1 block text-[10px] text-muted-foreground">review agent · {fmtTime(m.created_at)}</span>
								</div>
							</div>
						);
					})}
					{awaiting && (
						<div>
							<div className="flex items-center gap-2 pl-2 pt-1 text-xs text-muted-foreground">
								<span className="flex gap-1">
									<span className="size-1.5 animate-bounce rounded-full bg-blue-500 [animation-delay:-0.3s]"></span>
									<span className="size-1.5 animate-bounce rounded-full bg-blue-500 [animation-delay:-0.15s]"></span>
									<span className="size-1.5 animate-bounce rounded-full bg-blue-500"></span>
								</span>
								实时流式中…
							</div>
							{awaitSlow && (
								<p className="pl-2 pt-1 text-[11px] text-amber-600 dark:text-amber-400">
									等待较久——若 LLM 端点不通会一直等到超时，请检查 系统 → 设置 → LLM 配置的连通性
								</p>
							)}
						</div>
					)}
					<div ref={bottomRef} />
				</div>
			</ScrollArea>

			{/* ── 确认卡：停靠 composer 正上方（K15：写动作按钮确认制） ── */}
			{pending.length > 0 && (
				<div className="border-t bg-amber-50/50 px-4 pt-2.5 dark:bg-amber-950/15">
					{pending.map((p) => (
						<div key={p.id} className="mb-2.5 rounded-xl border border-amber-300/70 bg-card shadow-sm dark:border-amber-900/60">
							<div className="flex items-center gap-1.5 px-3.5 pt-2.5 text-xs font-bold text-amber-700 dark:text-amber-400">
								<TriangleAlertIcon className="h-3.5 w-3.5" />
								写动作待确认（intercept ask）
								<span className="ml-auto font-normal text-muted-foreground">超时自动拒绝</span>
							</div>
							<div className="px-3.5 pt-1 text-sm leading-6">review agent 请求执行：<b>{p.tool_name}</b></div>
							<div className="px-3.5 pb-2 pt-1">
								<pre className="max-h-36 overflow-auto rounded-md border bg-muted/50 px-2.5 py-1.5 text-xs text-foreground/80">
									{typeof p.tool_input === "string" ? p.tool_input : JSON.stringify(p.tool_input, null, 2)}
								</pre>
							</div>
							<div className="flex justify-end gap-2 border-t border-amber-100 px-3.5 py-2 dark:border-amber-950">
								<Button size="sm" variant="outline" className="border-red-200 text-red-600 hover:bg-red-50 dark:border-red-900 dark:hover:bg-red-950/40" onClick={() => decidePending(p, "denied")}>
									<XIcon className="mr-1 h-3.5 w-3.5" />拒绝
								</Button>
								<Button size="sm" className="bg-green-600 hover:bg-green-700" onClick={() => decidePending(p, "allowed")}>
									<CheckIcon className="mr-1 h-3.5 w-3.5" />确认执行
								</Button>
							</div>
						</div>
					))}
				</div>
			)}

			{/* ── composer（与其他会话窗一致的 InputGroup 样式） ── */}
			<div className="border-t p-3">
				<InputGroup className="min-h-9 has-disabled:opacity-100">
					<InputGroupTextarea
						rows={1}
						aria-label="和 review agent 对话"
						placeholder="和 review agent 讨论待审意图…"
						value={input}
						disabled={sending}
						onChange={(e) => setInput(e.target.value)}
						onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } }}
						className="max-h-36 min-h-9 overflow-y-auto"
					/>
					<InputGroupAddon align="block-end">
						<InputGroupButton
							className="ml-auto"
							size="icon-xs"
							onClick={send}
							disabled={!input.trim() || sending}
							title="发送消息"
							aria-label="发送"
						>
							{sending ? <Loader2Icon className="animate-spin" /> : <ArrowUpIcon />}
						</InputGroupButton>
					</InputGroupAddon>
				</InputGroup>
			</div>

			{/* ── 驳回对话框（理由必填） ── */}
			<AlertDialog open={!!rejectTarget} onOpenChange={(o) => !o && setRejectTarget(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>驳回意图</AlertDialogTitle>
						<AlertDialogDescription>驳回后意图退回修订（review_status=rejected，节点与血缘保留）：planner 会收到驳回理由，用 update_intent 修订该原稿后自动重新送审。</AlertDialogDescription>
					</AlertDialogHeader>
					{rejectTarget && (
						<div className="rounded-md border bg-muted px-2.5 py-2 text-sm leading-6">
							<b className="font-mono text-blue-600 dark:text-blue-400">#{rejectTarget.id}</b>　{rejectTarget.summary}
						</div>
					)}
					{/* AI 预审意见带入：驳回理由默认以预审意见为草稿，人可直接修改 */}
					{rejectTarget?.review_note ? (
						<div className="flex items-center gap-2 text-xs text-amber-700 dark:text-amber-400">
							<span className="rounded border-l-[3px] border-amber-500 bg-amber-50 px-2 py-0.5 dark:bg-amber-950/40">
								已自动带入 AI 预审意见，可直接修改
							</span>
							<Button
								type="button"
								size="sm"
								variant="ghost"
								className="h-6 px-2 text-xs text-amber-700 hover:bg-amber-50 dark:text-amber-400 dark:hover:bg-amber-950/40"
								onClick={() => setRejectReason(prefillRejectReason(rejectTarget))}
							>
								重新带入
							</Button>
						</div>
					) : null}
					{/* field-sizing-fixed：ui/textarea 默认 field-sizing-content 会随内容宽度生长，
					    带入的长预审意见会把滚动条/resize 手柄顶出对话框右边界 */}
					<Textarea
						className="mt-1 h-24 field-sizing-fixed"
						placeholder="说明为什么驳回，例如：方向判断错误 / 目标已失效 / 与其他意图重复…"
						value={rejectReason}
						onChange={(e) => setRejectReason(e.target.value)}
					/>
					<p className="text-xs text-muted-foreground">理由会以 revision_requested 事件发给 planner，指导它用 update_intent 修订这条意图后重送审。</p>
					<AlertDialogFooter>
						<Button variant="outline" onClick={() => setRejectTarget(null)}>取消</Button>
						<Button variant="destructive" disabled={!rejectReason.trim()} onClick={doReject}>确认驳回</Button>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}
