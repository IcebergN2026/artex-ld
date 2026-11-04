/**
 * activity-merge.ts：合并历史与实时活动流，按 seq 去重并保持顺序。
 * 主要函数/类型：
 *   - mergeActivities：按 seq 去重合并两段 Activity 并按 seq 排序
 */
import type { Activity } from "./types";

// History and live responses can overlap or arrive out of order. A persisted
// activity's seq is its identity; replay must not duplicate rows or token totals.
export function mergeActivities(current: Activity[], incoming: Activity[]): Activity[] {
  const bySeq = new Map<number, Activity>();
  for (const activity of current) bySeq.set(activity.seq, activity);
  for (const activity of incoming) bySeq.set(activity.seq, activity);
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq);
}
