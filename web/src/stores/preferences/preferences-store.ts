/**
 * preferences-store.ts：界面偏好的 Zustand vanilla store 定义。
 * 主要函数/类型：
 *   - PreferencesState：主题模式、预设、字体、布局、侧栏等偏好状态及 setter
 *   - createPreferencesStore：创建带默认值与 setter 的 preferences store
 */
import { createStore } from "zustand/vanilla";

import type { FontKey } from "@/lib/fonts/registry";
import type { ContentLayout, NavbarStyle, SidebarCollapsible, SidebarVariant } from "@/lib/preferences/layout";
import { PREFERENCE_DEFAULTS } from "@/lib/preferences/preferences-config";
import type { ResolvedThemeMode, ThemeMode, ThemePreset } from "@/lib/preferences/theme";

export type PreferencesState = {
  themeMode: ThemeMode;
  resolvedThemeMode: ResolvedThemeMode;
  themePreset: ThemePreset;
  font: FontKey;
  contentLayout: ContentLayout;
  navbarStyle: NavbarStyle;
  sidebarVariant: SidebarVariant;
  sidebarCollapsible: SidebarCollapsible;
  setThemeMode: (mode: ThemeMode) => void;
  setResolvedThemeMode: (mode: ResolvedThemeMode) => void;
  setThemePreset: (preset: ThemePreset) => void;
  setFont: (font: FontKey) => void;
  setContentLayout: (layout: ContentLayout) => void;
  setNavbarStyle: (style: NavbarStyle) => void;
  setSidebarVariant: (variant: SidebarVariant) => void;
  setSidebarCollapsible: (mode: SidebarCollapsible) => void;
  isSynced: boolean;
  setIsSynced: (val: boolean) => void;
};

export const createPreferencesStore = (init?: Partial<PreferencesState>) =>
  createStore<PreferencesState>()((set) => ({
    themeMode: init?.themeMode ?? PREFERENCE_DEFAULTS.theme_mode,
    resolvedThemeMode: init?.resolvedThemeMode ?? "light",
    themePreset: init?.themePreset ?? PREFERENCE_DEFAULTS.theme_preset,
    font: init?.font ?? PREFERENCE_DEFAULTS.font,
    contentLayout: init?.contentLayout ?? PREFERENCE_DEFAULTS.content_layout,
    navbarStyle: init?.navbarStyle ?? PREFERENCE_DEFAULTS.navbar_style,
    sidebarVariant: init?.sidebarVariant ?? PREFERENCE_DEFAULTS.sidebar_variant,
    sidebarCollapsible: init?.sidebarCollapsible ?? PREFERENCE_DEFAULTS.sidebar_collapsible,
    setThemeMode: (mode) => set({ themeMode: mode }),
    setResolvedThemeMode: (mode) => set({ resolvedThemeMode: mode }),
    setThemePreset: (preset) => set({ themePreset: preset }),
    setFont: (font) => set({ font }),
    setContentLayout: (layout) => set({ contentLayout: layout }),
    setNavbarStyle: (style) => set({ navbarStyle: style }),
    setSidebarVariant: (variant) => set({ sidebarVariant: variant }),
    setSidebarCollapsible: (mode) => set({ sidebarCollapsible: mode }),
    isSynced: init?.isSynced ?? false,
    setIsSynced: (val) => set({ isSynced: val }),
  }));
