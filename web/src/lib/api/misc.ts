// misc 域：工具执行历史（commands）、LLM 调用记录与按模型 token 聚合、一键更新。
/**
 * 主要函数/类型：
 *   - miscApi / MiscApi：杂项域 API 聚合对象及其类型
 *   - commands / commandStats：工具执行历史分页列表与各工具调用次数
 *   - llmRecords/llmRecordDetail/llmTasks/llmRecordsDeleteTask/tokensByModel：LLM 调用记录与按模型 token 聚合
 *   - checkUpdate / applyUpdate / rollbackUpdate：一键更新的检查、应用与回滚
 */
import { del, get, post } from "@/lib/api/http";
import type {
  CommandRecord,
  LLMRecordDetail,
  LLMRecordItem,
  LLMTask,
  ModelTokenStat,
  ToolStat,
  UpdateCheck,
} from "@/lib/types";

export const miscApi = {
  // ---- commands (tool execution history, any tool) ----
  commands: (params?: { task?: string; q?: string; page?: number; size?: number }) => {
    const sp = new URLSearchParams();
    if (params?.task) sp.set("task", params.task);
    if (params?.q) sp.set("q", params.q);
    sp.set("page", String(params?.page ?? 0));
    sp.set("size", String(params?.size ?? 50));
    return get<{ commands: CommandRecord[]; total: number }>(`/commands?${sp}`);
  },
  // 各工具调用次数；沿用列表的 task/q 筛选，统计的是整个结果集而非当前页。
  commandStats: (params?: { task?: string; q?: string }) => {
    const sp = new URLSearchParams();
    if (params?.task) sp.set("task", params.task);
    if (params?.q) sp.set("q", params.q);
    return get<{ stats: ToolStat[] }>(`/commands/stats?${sp}`);
  },

  // ---- LLM records ----
  llmRecords: (params?: { model?: string; session?: string; task?: string; page?: number; size?: number }) => {
    const sp = new URLSearchParams();
    if (params?.model) sp.set("model", params.model);
    if (params?.session) sp.set("session", params.session);
    if (params?.task) sp.set("task", params.task);
    if (params?.page !== undefined) sp.set("page", String(params.page));
    sp.set("size", String(params?.size ?? 50));
    return get<{ records: LLMRecordItem[]; total: number }>(`/llm/records?${sp}`);
  },
  llmRecordDetail: (id: number) => get<LLMRecordDetail>(`/llm/records/${id}`),
  llmTasks: () => get<{ tasks: LLMTask[] }>(`/llm/records/tasks`),
  llmRecordsDeleteTask: (task: string) => del<{ deleted: number }>(`/llm/records?task=${encodeURIComponent(task)}`),
  // 按模型聚合本任务的 token 用量（来自常开的 llm_usage 计量账本，逐次调用精确，
  // per-agent 绑定 / 轮询 / 中断消耗都覆盖）。
  tokensByModel: (task: string) =>
    get<{ models: ModelTokenStat[] }>(`/llm/records/by-model?task=${encodeURIComponent(task)}`),

  // ---- 一键更新 ----
  // 检查以后端为准：下载是后端做的，浏览器能连 GitHub 而服务器连不上的情况很常见
  // （服务器在内网、代理只配在浏览器上），那时点更新必然失败。
  // 后端对 GitHub 的查询结果有 30 分钟缓存（未认证的 GitHub API 是 60 次/小时/IP，
  // 顶栏每次整页加载都会查一次，不缓存会很快耗光配额）。force=true 强制回源，
  // 留给用户显式点「检查更新」时用。
  checkUpdate: (force = false) => get<UpdateCheck>(`/update/check${force ? "?force=1" : ""}`),
  // 202 即返回，实际下载在后台跑，进度走 /api/update/stream。
  applyUpdate: () => post<{ ok: boolean; target: string }>(`/update/apply`),
  rollbackUpdate: () => post<{ ok: boolean }>(`/update/rollback`),
};

export type MiscApi = typeof miscApi;
