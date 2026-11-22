/**
 * app-config.ts：应用级静态配置（名称、版本、版权、meta 信息）。
 * 主要函数/类型：
 *   - APP_CONFIG：聚合应用配置常量
 */
import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "artex",
  version: packageJson.version,
  copyright: `© ${currentYear}, artex.`,
  meta: {
    title: "artex — 自主渗透测试控制台",
    description: "LLM 驱动的自主渗透测试系统控制台",
  },
};
