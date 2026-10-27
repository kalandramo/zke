import { readFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// ZKE Server keeps a strict same-origin model: session and CSRF cookies are
// same-site, and the Server applies Fetch Metadata cross-origin protection.
// The dev server therefore proxies API traffic instead of enabling CORS, and
// must not rewrite the Origin header (`changeOrigin: false`).
const serverTarget = process.env.ZKE_SERVER_URL ?? "http://127.0.0.1:8080";

const proxiedPrefixes = ["/api", "/healthz", "/readyz", "/agent-install"];

/**
 * kit-lab 顶栏要显示真实版本号。该包 exports 白名单里没有 ./package.json，客户端
 * 直接 import 它会在构建期解析失败，所以只能在 Node 侧读文件、用 define 注入常量。
 * 读的是安装事实而非 package.json 里的 pin：pin 写 0.5.0 而 lockfile 解析到别的版本时，
 * 页面上显示的是真正跑起来的那一个。
 */
const desktopKitVersion: string = JSON.parse(
  readFileSync(
    fileURLToPath(new URL("./node_modules/@kalandramo/desktop-kit/package.json", import.meta.url)),
    "utf8",
  ),
).version;

export default defineConfig({
  define: {
    __DESKTOP_KIT_VERSION__: JSON.stringify(desktopKitVersion),
  },
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: Object.fromEntries(
      proxiedPrefixes.map((prefix) => [
        prefix,
        {
          target: serverTarget,
          changeOrigin: false,
          // Keep SSE unbuffered and forward WebSocket Upgrade for Pod terminal
          // sessions through the same-origin development endpoint.
          ws: true,
        },
      ]),
    ),
  },
  build: {
    // Immutable asset names; index.html itself must not be cached long term.
    assetsDir: "assets",
    sourcemap: true,
    rollupOptions: {
      // desktop-kit 集成实验页（desktop-kit 分支专用，见 DESKTOP-KIT-INTEGRATION.md）。
      input: {
        main: fileURLToPath(new URL("./index.html", import.meta.url)),
        "kit-lab": fileURLToPath(new URL("./kit-lab.html", import.meta.url)),
        // 真实桌面试跑入口：11 个 ZKE 应用挂到 kit 外壳上，夹具身份，见 src/desktop-trial。
        "desktop-trial": fileURLToPath(new URL("./desktop-trial.html", import.meta.url)),
        // 生产桌面的夹具入口：挂的是真的 src/desktop/Desktop.tsx，见 src/desktop-prod。
        "desktop-prod": fileURLToPath(new URL("./desktop-prod.html", import.meta.url)),
      },
    },
  },
});
