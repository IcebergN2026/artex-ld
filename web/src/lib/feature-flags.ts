/**
 * 前端功能开关（编译期常量）。
 *
 * INTRANET_CREDENTIALS_UI：内网凭据（asset_credentials / asset_credential_links）的
 * 前端展示总开关。关闭时隐藏「内网凭据」标签页、宿主表的凭据汇总与抽屉凭据段、
 * 服务表的「认证」列；后端 API、数据库、worker 的 record_credential /
 * mark_credential_verified 工具与 planner 侧行为均不受影响。
 * 需要重新开放时置 true 并重新构建前端与二进制。
 */
export const INTRANET_CREDENTIALS_UI = false;
