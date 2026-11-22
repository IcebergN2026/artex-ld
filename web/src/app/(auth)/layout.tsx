/**
 * layout.tsx：(auth) 路由组布局，当前为透传壳，仅包裹登录/初始化页面。
 * 主要函数/类型：
 *   - AuthLayout：直接渲染 children
 */
import type { ReactNode } from "react";

export default function AuthLayout({ children }: { readonly children: ReactNode }) {
  return <>{children}</>;
}
