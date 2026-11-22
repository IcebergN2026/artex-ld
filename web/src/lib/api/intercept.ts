// intercept 域：全局拦截规则、资产拦截黑名单、人工/模型审批（pending/history/
// task 维度）、全局工具拦截范围与 LLM 兜底审批配置。
/**
 * 主要函数/类型：
 *   - interceptApi / InterceptApi：拦截域 API 聚合对象及其类型
 *   - interceptRules 系列：全局拦截规则 CRUD 与开关
 *   - assetInterceptRules 系列：资产拦截黑名单 CRUD 与开关
 *   - interceptPending/interceptDecide/interceptHistory/interceptTask 系列：人工/模型审批查询与决定
 *   - interceptGetToolConfig/interceptSetToolConfig：全局工具拦截范围
 *   - interceptGetJudgeConfig/interceptSetJudgeConfig/interceptJudgeUsage：LLM 兜底审批配置与用量
 */

import { arr, del, get, getToken, interceptPageQuery, post, put } from "@/lib/api/http";
import { MOCK } from "@/lib/mock/enabled";
import type {
  AssetInterceptRule,
  InterceptApprovalFilter,
  InterceptApprovalRow,
  InterceptDetail,
  InterceptPending,
  InterceptRule,
  JudgeConfig,
  JudgeUsage,
} from "@/lib/types";

export const interceptApi = {
  // ---- intercept rules ----
  interceptRules: () => get<{ rules: InterceptRule[] }>("/intercept/rules").then((r) => arr(r.rules)),
  // 批量操作:给所有未分类规则追加该标签。
  interceptTagUntagged: async (name: string): Promise<number> => {
    if (MOCK) return 0;
    const r = await post<{ rules_affected: number }>(`/intercept/tags/${encodeURIComponent(name)}/tag-untagged`, {});
    return r.rules_affected;
  },
  // ---- 规则标签管理 ----
  interceptListTags: async (): Promise<string[]> => {
    if (MOCK) return ["WebShell 管理", "命令执行"];
    return get<{ tags: string[] }>("/intercept/tags").then((r) => arr(r.tags));
  },
  interceptSaveTags: async (tags: string[]): Promise<string[]> => {
    if (MOCK) return tags;
    return put<{ tags: string[] }>("/intercept/tags", { tags }).then((r) => arr(r.tags));
  },
  interceptRenameTag: async (name: string, newName: string): Promise<void> => {
    if (MOCK) return;
    await post(`/intercept/tags/${encodeURIComponent(name)}/rename`, { new_name: newName });
  },
  // 批量操作:把该标签下所有规则的适用 agent 覆盖为 agents。
  interceptApplyAgentsForTag: async (name: string, agents: string[]): Promise<number> => {
    if (MOCK) return 0;
    const r = await post<{ rules_affected: number }>(`/intercept/tags/${encodeURIComponent(name)}/apply-agents`, { agents });
    return r.rules_affected;
  },
  interceptDeleteTag: async (name: string): Promise<void> => {
    if (MOCK) return;
    await del<{ deleted: string }>(`/intercept/tags/${encodeURIComponent(name)}`);
  },
  // 批量操作:给多选规则追加/覆盖标签,返回受影响条数。
  interceptBatchSetTags: async (ids: number[], tags: string[], mode: "replace" | "append"): Promise<number> => {
    if (MOCK) return ids.length;
    const r = await post<{ affected: number }>("/intercept/rules/tags", { ids, tags, mode });
    return r.affected;
  },
  createInterceptRule: (rule: Omit<InterceptRule, "id" | "created_at" | "updated_at">) =>
    post<InterceptRule>("/intercept/rules", rule),
  updateInterceptRule: (id: number, rule: Omit<InterceptRule, "id" | "created_at" | "updated_at">) =>
    put<InterceptRule>(`/intercept/rules/${id}`, rule),
  deleteInterceptRule: (id: number) => del<{ deleted: number }>(`/intercept/rules/${id}`),
  toggleInterceptRule: (id: number, enabled: boolean) =>
    post<{ ok: boolean; enabled: boolean }>(`/intercept/rules/${id}/toggle`, { enabled }),

  // ---- asset intercept rules（资产拦截：全局黑名单） ----
  assetInterceptRules: () => get<{ rules: AssetInterceptRule[] }>("/asset-intercept/rules").then((r) => arr(r.rules)),
  createAssetInterceptRule: (rule: Pick<AssetInterceptRule, "enabled" | "kind" | "pattern" | "note">) =>
    post<AssetInterceptRule>("/asset-intercept/rules", rule),
  updateAssetInterceptRule: (id: number, rule: Pick<AssetInterceptRule, "enabled" | "kind" | "pattern" | "note">) =>
    put<AssetInterceptRule>(`/asset-intercept/rules/${id}`, rule),
  deleteAssetInterceptRule: (id: number) => del<{ deleted: number }>(`/asset-intercept/rules/${id}`),
  toggleAssetInterceptRule: (id: number, enabled: boolean) =>
    post<{ ok: boolean; enabled: boolean }>(`/asset-intercept/rules/${id}/toggle`, { enabled }),

  // ---- intercept pending (ask) ----
  interceptPending: () => get<{ pending: InterceptPending[] }>("/intercept/pending").then((r) => arr(r.pending)),
  interceptGetOne: (id: number) => get<InterceptPending>(`/intercept/pending/${id}`),
  interceptDecide: (id: number, decision: "allowed" | "denied") =>
    post<{ ok: boolean }>(`/intercept/pending/${id}/decide`, { decision }),
  interceptExecution: (id: number, conversationId?: number) =>
    get<import("@/lib/types").InterceptExecution>(
      `/intercept/history/${id}/execution${conversationId ? `?conversation=${conversationId}` : ""}`,
    ),
  interceptDetail: (id: number) => get<InterceptDetail>(`/intercept/history/${id}`),
  interceptHistory: () => get<{ items: InterceptApprovalRow[] }>("/intercept/history").then((r) => arr(r.items)),
  interceptHistoryPage: (page = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
    get<{ items: InterceptApprovalRow[]; total?: number }>(
      `/intercept/history?${interceptPageQuery(page, size, filter)}`,
    ).then((r) => ({
      items: arr(r.items),
      total: r.total ?? r.items?.length ?? 0,
    })),
  interceptTask: (taskId: string) =>
    get<{ items: InterceptApprovalRow[] }>(`/intercept/task/${taskId}`).then((r) => arr(r.items)),
  interceptTaskPage: (taskId: string, page = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
    get<{ items: InterceptApprovalRow[]; total?: number }>(
      `/intercept/task/${encodeURIComponent(taskId)}?${interceptPageQuery(page, size, filter)}`,
    ).then((r) => ({
      items: arr(r.items),
      total: r.total ?? r.items?.length ?? 0,
    })),

  // ---- intercept tool-config (全局工具拦截范围) ----
  interceptGetToolConfig: async (): Promise<{ enabled_tools: string[] }> => {
    if (MOCK) return { enabled_tools: ["bash"] };
    const token = getToken();
    const r = await fetch("/api/intercept/tool-config", {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
  interceptSetToolConfig: async (enabledTools: string[]): Promise<void> => {
    if (MOCK) return;
    const token = getToken();
    const r = await fetch("/api/intercept/tool-config", {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ enabled_tools: enabledTools }),
    });
    if (!r.ok) throw new Error(await r.text());
  },

  // ---- intercept LLM judge (模型兜底审批,全局配置) ----
  interceptGetJudgeConfig: () => get<JudgeConfig>("/intercept/judge"),
  interceptSetJudgeConfig: (cfg: JudgeConfig) => put<{ ok: boolean }>("/intercept/judge", cfg),
  interceptJudgeUsage: (days = 30) => get<JudgeUsage>(`/intercept/judge/usage?days=${days}`),
};

export type InterceptApi = typeof interceptApi;
