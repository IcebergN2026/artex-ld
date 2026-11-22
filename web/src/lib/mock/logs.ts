// MOCK 构建下 system/logs 页的本地示例日志（不连接后端 SSE）。
// 从 page.tsx 内联提出至此：page 里动态 import 本模块，非 MOCK 构建由
// next.config.mjs 的 resolveAlias 把它替换为 logs-off 空桩，示例数据不进生产 bundle。
/**
 * 主要函数/类型：
 *   - MOCK_LOGS：system/logs 页的本地示例日志行（MOCK 构建下替代后端 SSE）
 */
import type { LogLine } from "@/lib/types";

export const MOCK_LOGS: LogLine[] = [
  { seq: 1, ts: "2026-07-26T03:55:00Z", level: "info", tag: "engine", text: "artex v0.1.0 backend listening on :8787 (workers=3)" },
  { seq: 2, ts: "2026-07-26T03:55:01Z", level: "info", tag: "config", text: "LLM configured from DB: anthropic / claude-opus-4-8" },
  { seq: 3, ts: "2026-07-26T03:56:10Z", level: "info", tag: "planner", text: "task t-acme-web: 第 3 轮规划，生成意图 i-4" },
  { seq: 4, ts: "2026-07-26T03:57:00Z", level: "warn", tag: "guard", text: "block bash: 目标越界 out.evil.example 不在 scope 内" },
  { seq: 5, ts: "2026-07-26T03:57:30Z", level: "info", tag: "work#1", text: "report_finding: Default Credentials (high) 已落库" },
  { seq: 6, ts: "2026-07-26T03:58:20Z", level: "error", tag: "work#3", text: "intercept: mysqldump 命中破坏性规则，等待人工审批" },
];
