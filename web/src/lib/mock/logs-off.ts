// 生产构建替代 lib/mock/logs 的空桩：next.config.mjs 在非 MOCK 构建通过 resolveAlias
// 把 "@/lib/mock/logs" 指到本模块。运行时永远走不到这里（只有 MOCK 判定为 true 才会
// 加载，而那种构建不做本 alias）。
/**
 * 主要函数/类型：
 *   - MOCK_LOGS：空日志数组桩（替代 mock/logs 的示例数据）
 */
import type { LogLine } from "@/lib/types";

export const MOCK_LOGS: LogLine[] = [];
