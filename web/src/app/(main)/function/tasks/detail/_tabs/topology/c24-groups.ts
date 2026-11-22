/**
 * c24-groups.ts：按 /24 网段对内网拓扑节点自动归组。
 * 主要函数：
 *   - c24Name：IPv4 → "a.b.c.0/24"；非 IPv4 返回 null
 *   - planC24Groups：未分组节点按 /24 聚类，成员 ≥2 的子网按名称排序返回
 *   - isC24Grouped / markC24Grouped：安全读写一次性归组标记
 *   - applyC24Grouping：建/复用同名分组并把成员移入，返回后端最新整图
 * 组创建几何复用 nextGroupPlacement（右侧落位）+ 默认框尺寸，成员坐标随后由
 * autoLayout 写入；归组全部在前端完成，不动后端 fact 建边逻辑。
 */
import { api } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import type { TopologyGraph } from "@/lib/types";

import { GROUP_DEFAULT_H, GROUP_DEFAULT_W, nextGroupPlacement } from "./graph-model";

function c24GroupedKey(taskId: string): string {
  return `topology-c24-grouped-${taskId}`;
}

export function isC24Grouped(taskId: string): boolean {
  return getLocalStorageValue(c24GroupedKey(taskId)) === "1";
}

export function markC24Grouped(taskId: string): void {
  setLocalStorageValue(c24GroupedKey(taskId), "1");
}

export function c24Name(ip: string): string | null {
  const parts = ip.trim().split(".");
  if (parts.length !== 4) return null;
  if (!parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255)) return null;
  return `${parts[0]}.${parts[1]}.${parts[2]}.0/24`;
}

export function planC24Groups(graph: TopologyGraph): Array<{ name: string; members: string[] }> {
  const bySubnet = new Map<string, string[]>();
  for (const node of graph.nodes) {
    if (node.group_id != null) continue;
    const name = c24Name(node.ip);
    if (!name) continue;
    const list = bySubnet.get(name) ?? [];
    list.push(node.ip);
    bySubnet.set(name, list);
  }
  return [...bySubnet.entries()]
    .filter(([, members]) => members.length >= 2)
    .map(([name, members]) => ({ name, members: members.sort() }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

export async function applyC24Grouping(taskId: string, graph: TopologyGraph): Promise<TopologyGraph> {
  let current = graph;
  for (const { name, members } of planC24Groups(graph)) {
    let group = current.groups.find((g) => g.name === name);
    if (!group) {
      current = await api.addTopologyGroup(taskId, {
        name,
        ...nextGroupPlacement(current),
        width: GROUP_DEFAULT_W,
        height: GROUP_DEFAULT_H,
      });
      group = current.groups.find((g) => g.name === name);
    }
    if (!group) continue;
    for (const ip of members) {
      current = await api.patchTopologyNode(taskId, ip, { group_id: group.id, x: 0, y: 0 });
    }
  }
  return current;
}
