// Mock 开关。构建期注入的公开变量（NEXT_PUBLIC_ 前缀才在浏览器可读）。
// Vercel 上设 NEXT_PUBLIC_MOCK=1 即整站走 mock、无需后端。
/**
 * 主要函数/类型：
 *   - MOCK：是否启用 mock 演示模式的构建期布尔开关
 */
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
