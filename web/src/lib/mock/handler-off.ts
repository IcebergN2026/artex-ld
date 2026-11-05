// 生产构建替代 lib/mock/handler 的空实现：next.config.mjs 在非 MOCK 构建通过
// turbopack resolveAlias 把 "@/lib/mock/handler" 指到本模块，配合 lib/api/http.ts
// 的动态 import，使 6800 行 demo 数据完全不进生产 bundle（Turbopack 不会为未设置的
// NEXT_PUBLIC_* 变量做死代码折叠，单靠 if 分支消不掉该 import() 依赖）。
// 运行时永远走不到这里：只有 MOCK 判定为 true 才会加载，而那种构建不做本 alias。
/**
 * 主要函数/类型：
 *   - mockHandle：永不返回的空实现（抛错占位）
 */
export async function mockHandle(): Promise<never> {
  throw new Error("mock handler is only available in NEXT_PUBLIC_MOCK=1 builds");
}
