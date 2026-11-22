// 生产构建替代 lib/mock/report 的空桩：next.config.mjs 在非 MOCK 构建通过 resolveAlias
// 把 "@/lib/mock/report" 指到本模块。运行时永远走不到这里（只有 MOCK 判定为 true 才会
// 加载，而那种构建不做本 alias）。
/**
 * 主要函数/类型：
 *   - mockReport：非 MOCK 构建的空桩（调用即抛错）
 */
export function mockReport(): string {
  throw new Error("mock report is only available in NEXT_PUBLIC_MOCK=1 builds");
}
