/**
 * 主要函数/类型：
 *   - fontRegistry / FontKey：可用字体注册表（key → next/font 实例）
 *   - fontKeys：全部合法字体 key（供持久化值校验使用）
 *   - fontVars：全部字体的 CSS 变量名列表
 *   - fontOptions：字体下拉选项（label + variable）
 */
import { GeistMono } from "geist/font/mono";
import { GeistPixelSquare } from "geist/font/pixel";
import { GeistSans } from "geist/font/sans";

export const fontRegistry = {
  geist: { label: "Geist", font: GeistSans },
  geistMono: { label: "Geist Mono", font: GeistMono },
  geistPixelSquare: { label: "Geist Pixel Square", font: GeistPixelSquare },
} as const;

export type FontKey = keyof typeof fontRegistry;

export const fontKeys = Object.keys(fontRegistry) as FontKey[];

export const fontVars = (Object.values(fontRegistry) as Array<(typeof fontRegistry)[FontKey]>)
  .map((f) => f.font.variable)
  .join(" ");

export const fontOptions = (
  Object.entries(fontRegistry) as [FontKey, { label: string; font: { variable: string } }][]
).map(([key, f]) => ({
  key,
  label: f.label,
  variable: f.font.variable,
}));
