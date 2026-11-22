/**
 * utils.ts：通用工具函数集合。
 * 主要函数/类型：
 *   - cn：clsx + tailwind-merge 的类名合并
 *   - radixOverlayWasOpenAtPointerDown：抽屉内弹层打开时的外部点击关闭判定辅助
 *   - copyText / getInitials / fmtBytes / fmtTokens / fmtTime：复制、姓名缩写与格式化工具
 *   - statusTone：HTTP 状态码映射 tailwind 文本色
 */
import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// 抽屉/对话框(Sheet/Dialog)的 onInteractOutside 关闭判定辅助。
//
// 背景:抽屉内的 Radix 弹层(Select 下拉、DropdownMenu、Popover 等)会 portal 到抽屉
// 之外。开着弹层时点遮罩/抽屉外想收起它,这一次 pointerdown 会被 Select 和 Sheet 两个
// DismissableLayer 同时处理;Select 先关闭且是 discrete 事件、React 会同步 flush,于是
// 轮到 Sheet 的处理器时弹层的 data-state 早已翻成 closed —— 在"当下"检测弹层是否打开
// 天然不可靠(实测已验证)。
//
// 正确做法:Radix 的 pointerdown 监听在冒泡阶段;我们在 capture 阶段(早于它)先把
// "此刻有没有弹层开着"记录下来,onInteractOutside 再读这个记录值来决定是否放行关闭。
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture:抢在 Radix 冒泡阶段的 pointerdown 处理器之前记录
  );
}

// radixOverlayWasOpenAtPointerDown 返回"最近一次 pointerdown 发生时是否有 Radix 弹层
// 开着"。抽屉/对话框据此:开着弹层时点遮罩 → 只收弹层、不关自身。
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// copyText 把文本写入剪贴板,返回是否成功。
// 背景:navigator.clipboard 仅在安全上下文(HTTPS / localhost)可用;通过 IP + HTTP
// 访问时它为 undefined,此时降级到 execCommand("copy")。
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // 继续走降级方案
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

// fmtBytes renders a human file size: ≥1 MiB → "x.x MB"，≥1 KiB → "x.x KB"，否则 "N B"。
// 附件 chips、上传清单等共用；需要 GB 或空值语义的版本（如 traffic 页）保留局部实现。
export function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M)。
export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

// fmtTime 把后端时间戳格式化为 zh-CN 的 月/日 时:分:秒（列表与详情共用）。
export function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// statusTone 按 HTTP 状态码返回 tailwind 文本色类（资产/流量/报文查看共用）。
export function statusTone(status: number) {
  if (status >= 500) return "text-red-500";
  if (status >= 400) return "text-amber-500";
  if (status >= 300) return "text-blue-500";
  if (status >= 200) return "text-emerald-500";
  return "text-muted-foreground";
}
