/**
 * http.ts：真实后端客户端传输层，/api/* 由 next.config rewrites 代理到 Go 后端。
 * 域方法位于同目录域文件并在 lib/api.ts 聚合，http/sseUrl 由聚合面 re-export。
 * 主要函数/类型：
 *   - http / get / post / put / patch / del：带鉴权与统一错误处理的 fetch 封装（MOCK 构建短路到 mock handler）
 *   - sseUrl：构建 SSE 流地址（生产同源、开发直连 :8787）
 *   - getToken：读取 localStorage 中的登录 token
 *   - arr / tq / findingFilterParams / interceptPageQuery：空数组兜底、task 查询串与筛选序列化小工具
 */

import type { FindingQuery, InterceptApprovalFilter } from "@/lib/types";

export function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem("artex_token");
}

export async function http<T>(path: string, init?: RequestInit): Promise<T> {
  // 动态 import 让 6800 行 mock 数据只在 MOCK 构建进 bundle：此处必须内联
  // process.env 判定（而非经 lib/mock/enabled 的 MOCK 常量）。默认 Turbopack
  // 构建由 next.config.mjs 的 resolveAlias 把 mock handler 换成空桩；显式
  // webpack 构建则由 DefinePlugin 把它折成 if(false)，两种构建都能消除该
  // import() 依赖。
  if (process.env.NEXT_PUBLIC_MOCK === "1")
    return (await import("@/lib/mock/handler")).mockHandle<T>(init?.method ?? "GET", path, init?.body ?? null);
  const token = getToken();
  const r = await fetch(`/api${path}`, {
    ...init,
    headers: {
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers as Record<string, string> | undefined),
    },
  });
  if (r.status === 401) {
    if (typeof window !== "undefined") {
      localStorage.removeItem("artex_token");
      document.cookie = "artex_token=; path=/; max-age=0";
      window.location.href = "/login";
    }
    throw new Error("未授权");
  }
  if (!r.ok) {
    const fallback = `${init?.method ?? "GET"} ${path}: ${r.status}`;
    let message = fallback;
    try {
      const payload = (await r.json()) as { error?: unknown };
      if (typeof payload.error === "string" && payload.error.trim()) {
        message = payload.error.trim();
      }
    } catch {
      // Keep the status-based fallback for empty or non-JSON error responses.
    }
    throw new Error(message);
  }
  if (r.status === 204) return undefined as T;
  return r.json();
}

// sseUrl 构建 SSE 地址。同源流由浏览器携带登录 cookie，避免把 JWT 放进
// URL 查询串而泄漏到访问日志；仅跨源、无法使用该 cookie 时才附加 token。
export function sseUrl(path: string): string {
  const base =
    process.env.NEXT_PUBLIC_SSE_BASE ??
    (process.env.NODE_ENV !== "production" && typeof window !== "undefined"
      ? `${window.location.protocol}//${window.location.hostname}:8787`
      : "");
  const token = base !== "" ? getToken() : null;
  const sep = path.includes("?") ? "&" : "?";
  return token ? `${base}${path}${sep}token=${encodeURIComponent(token)}` : `${base}${path}`;
}

export const get = <T>(p: string) => http<T>(p);
export const post = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "POST", body: body ? JSON.stringify(body) : undefined });
export const put = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "PUT", body: body ? JSON.stringify(body) : undefined });
export const patch = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "PATCH", body: body ? JSON.stringify(body) : undefined });
export const del = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "DELETE", body: body ? JSON.stringify(body) : undefined });

// Go serializes nil slices as JSON null — coerce to [].
export const arr = <T>(x: T[] | null | undefined): T[] => x ?? [];
export const tq = (task?: string, sep: "?" | "&" = "?") => (task ? `${sep}task=${encodeURIComponent(task)}` : "");

// findingFilterParams 把发现页的筛选条件序列化成 query string。列表 / 分组 /
// 资产树 / 导出共用同一份,新增筛选项只改这里(后端也只解析这一份)。
export function findingFilterParams(q: Omit<FindingQuery, "page" | "pageSize">): URLSearchParams {
  const p = new URLSearchParams();
  if (q.severity && q.severity !== "all") p.set("severity", q.severity);
  if (q.status && q.status !== "all") p.set("status", q.status);
  if (q.vulnclass && q.vulnclass !== "all") p.set("vulnclass", q.vulnclass);
  if (q.task && q.task !== "all") p.set("task_id", q.task);
  if (q.query?.trim()) p.set("q", q.query.trim());
  if (q.sort) p.set("sort", q.sort);
  if (q.assetScope) p.set("asset_scope", q.assetScope);
  if (q.assetIDs?.length) p.set("asset_ids", q.assetIDs.join(","));
  return p;
}

export function interceptPageQuery(page: number, size: number, filter: InterceptApprovalFilter) {
  const query = new URLSearchParams({ page: String(page), size: String(size) });
  if (filter.status) query.set("status", filter.status);
  if (filter.decision_source) query.set("decision_source", filter.decision_source);
  return query.toString();
}
