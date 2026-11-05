// mockReport returns a canned Markdown report for the demo.
// 从 lib/api/llm-agents.ts 提出至此：report() 里动态 import 本模块，非 MOCK 构建由
// next.config.mjs 的 resolveAlias 把它替换为 report-off 空桩，demo 报告不进生产 bundle。
/**
 * 主要函数/类型：
 *   - mockReport：返回预置的 Markdown 渗透测试报告文本
 */
export function mockReport(_task?: string): string {
  return `# artex 渗透测试报告 — Acme Corp

## 概览
- 范围：acme.com（含 www / admin / api / shop / vpn 子域）
- 已确认发现：6 项（高危 3 · 中危 3 · 低危 2）
- 引擎模式：exploring

## 关键发现
1. **[高] 后台默认口令** admin.acme.com admin/admin123 → 可完全接管后台。
2. **[高] SQL 注入** www.acme.com/search?q= → 可读取 acme_prod 库。
3. **[高] IDOR** api.acme.com/v1/orders?id= → 可越权读取他人订单（含手机号/地址）。
4. **[中] 反射型 XSS**、**暴露 .git 源码**、**登录无速率限制**。

## 建议
- 后台强制改密 + 启用 MFA、封禁默认口令。
- search 接口参数化查询、输出编码。
- API 增加对象级授权校验（IDOR）、更换强 JWT 密钥。

> （demo）本报告由 mock 数据生成，仅用于界面演示。`;
}
