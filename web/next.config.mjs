import { fileURLToPath } from "node:url";

// 静态导出：`NEXT_EXPORT=1 next build` 产出纯静态目录到 web/out，可直接丢进
// nginx web 根目录运行。开发(next dev)不设该变量，保留 /api 反代与热更新。
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo：整站走 mock，无后端，无需 /api 反代。
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

function environmentConfig(exportMode, mockMode) {
  if (exportMode) {
    return {
      // 纯静态导出：无 Node 运行时；图片不经优化；每个路由产出 <route>/index.html。
      output: "export",
      images: { unoptimized: true },
      trailingSlash: true,
    };
  }
  if (mockMode) {
    // Vercel mock demo：无后端，不需要 /api 反代。
    return { images: { unoptimized: true } };
  }
  return {
    // 开发：把 /api/* 反代到 Go 后端（默认 :8787，可用 AUTOPENTEST_API 覆盖）。
    async rewrites() {
      const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
      return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
    },
  };
}

/** @type {import('next').NextConfig} */
const nextConfig = {
  // 避免父目录的 lockfile 影响根目录推断及资源路径生成。
  turbopack: {
    root: fileURLToPath(new URL(".", import.meta.url)),
    // 非 MOCK 构建：把 mock handler alias 到空实现。lib/api/http.ts 的 MOCK 分支是
    // 动态 import，Turbopack 对未设置的 NEXT_PUBLIC_* 不做死代码折叠，单靠 if(false)
    // 消不掉该 import() 依赖；alias 后生产 chunk 只含这个桩，demo 数据不进 bundle。
    ...(isMock
      ? {}
      : {
          resolveAlias: {
            // 注：Windows 上 Turbopack 不接受绝对路径 alias 目标，用 @ 请求串（tsconfig paths 解析）。
            "@/lib/mock/handler": "@/lib/mock/handler-off",
            "@/lib/mock/logs": "@/lib/mock/logs-off",
            "@/lib/mock/report": "@/lib/mock/report-off",
          },
        }),
  },
  reactCompiler: true,
  // 允许从局域网 IP 访问 dev 资源（HMR），按需增删。
  // dev 阶段放开任意 IPv4 来源访问 /_next/* 与 HMR（局域网 IP 变动也不受影响）。
  // 注意：Next 出于安全禁止裸 "*"，需用分段通配；"*.*.*.*" 匹配任意 IPv4。
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...environmentConfig(isExport, isMock),
  // 仅显式 webpack 构建（next build --webpack）会调用此回调；默认的 Turbopack
  // 构建不调用，mock 排除由上面的 turbopack.resolveAlias 负责。
  webpack: (config, { webpack }) => {
    // 强制内联 NEXT_PUBLIC_MOCK：未设置时 Next 不会替换 process.env 引用，导致
    // lib/api/http.ts 里 `if (process.env.NEXT_PUBLIC_MOCK === "1") import(...)`
    // 无法折成死分支、6800 行 mock 数据被动态 import 带进生产 bundle。
    // 显式 DefinePlugin 后生产构建为 if(false)（import 被消除），MOCK 构建为 if(true)。
    config.plugins.push(
      new webpack.DefinePlugin({
        "process.env.NEXT_PUBLIC_MOCK": JSON.stringify(process.env.NEXT_PUBLIC_MOCK),
      }),
    );
    return config;
  },
};

export default nextConfig;
