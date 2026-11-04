/**
 * layout.ts：布局相关偏好的选项常量与取值类型定义。
 * 主要函数/类型：
 *   - SIDEBAR_VARIANT_OPTIONS / SidebarVariant：侧边栏形态选项
 *   - SIDEBAR_COLLAPSIBLE_OPTIONS / SidebarCollapsible：折叠方式选项
 *   - CONTENT_LAYOUT_OPTIONS / ContentLayout：内容宽度选项
 *   - NAVBAR_STYLE_OPTIONS / NavbarStyle：顶栏样式选项
 */
// Sidebar Variant
export const SIDEBAR_VARIANT_OPTIONS = [
  { label: "Sidebar", value: "sidebar" },
  { label: "Inset", value: "inset" },
  { label: "Floating", value: "floating" },
] as const;
export const SIDEBAR_VARIANT_VALUES = SIDEBAR_VARIANT_OPTIONS.map((v) => v.value);
export type SidebarVariant = (typeof SIDEBAR_VARIANT_VALUES)[number];

// Sidebar Collapsible
export const SIDEBAR_COLLAPSIBLE_OPTIONS = [
  { label: "Icon", value: "icon" },
  { label: "Offcanvas", value: "offcanvas" },
] as const;
export const SIDEBAR_COLLAPSIBLE_VALUES = SIDEBAR_COLLAPSIBLE_OPTIONS.map((v) => v.value);
export type SidebarCollapsible = (typeof SIDEBAR_COLLAPSIBLE_VALUES)[number];

// Content Layout
export const CONTENT_LAYOUT_OPTIONS = [
  { label: "Centered", value: "centered" },
  { label: "Full Width", value: "full-width" },
] as const;
export const CONTENT_LAYOUT_VALUES = CONTENT_LAYOUT_OPTIONS.map((v) => v.value);
export type ContentLayout = (typeof CONTENT_LAYOUT_VALUES)[number];

// Navbar Style
export const NAVBAR_STYLE_OPTIONS = [
  { label: "Sticky", value: "sticky" },
  { label: "Scroll", value: "scroll" },
] as const;
export const NAVBAR_STYLE_VALUES = NAVBAR_STYLE_OPTIONS.map((v) => v.value);
export type NavbarStyle = (typeof NAVBAR_STYLE_VALUES)[number];
