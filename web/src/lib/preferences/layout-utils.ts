/**
 * layout-utils.ts：把布局偏好值应用到 DOM（根元素 data-* 属性）。
 * 主要函数/类型：
 *   - applyContentLayout / applyNavbarStyle：内容宽度与顶栏样式
 *   - applySidebarVariant / applySidebarCollapsible：侧边栏形态与折叠方式
 *   - applyFont：切换字体变量
 */
export function applyContentLayout(value: "centered" | "full-width") {
  const root = document.documentElement;
  root.setAttribute("data-content-layout", value);
}

export function applyNavbarStyle(value: "sticky" | "scroll") {
  const root = document.documentElement;
  root.setAttribute("data-navbar-style", value);
}

export function applySidebarVariant(value: string) {
  const root = document.documentElement;
  root.setAttribute("data-sidebar-variant", value);
}

export function applySidebarCollapsible(value: string) {
  const root = document.documentElement;
  root.setAttribute("data-sidebar-collapsible", value);
}

export function applyFont(value: string) {
  const root = document.documentElement;
  root.setAttribute("data-font", value);
}
