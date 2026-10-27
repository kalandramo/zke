import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// desktop-kit 的唯一 CSS 出口，与 src/main.tsx 同一份；瓦片令牌在它之后。
import "@kalandramo/desktop-kit/styles.css";
import "@/styles/app-accents.css";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@kalandramo/desktop-kit/ui";

import { SessionProvider } from "@/auth/SessionProvider";
import { AppToaster } from "@/components/common/toaster";
import { Desktop } from "@/desktop/Desktop";
import {
  readProdLayoutChoice,
  seedProdFixtureCache,
  writeStoredLayout,
  SEEDED_QUERY_KEYS,
} from "./fixture";

/**
 * 生产桌面的夹具入口：挂的是**真的** `src/desktop/Desktop.tsx`。
 *
 * 与 `desktop-trial` 的分工是一条硬边界，不是一种风格差异。试跑页是「同一套桥接 + 同一
 * 套 kit 组件」的同构复刻，它证明不了 `Desktop.tsx` 里那些只有真入口才会走到的路径——
 * restore 门控、按用户重建 runtime、深链、动态清单与 restore 的先后、显式 persist。这一
 * 页把那些路径变成直接覆盖：入口文件里没有任何桌面逻辑，只有「把夹具塞进真实接缝，然后
 * 渲染真实组件」。
 *
 * 硬边界：`src/desktop/**`、`auth/**`、`api/**`、`scope/**`、`apps/**` 一行未改，也不
 * 是为了这一页而加的。接缝只有三个——Query 缓存（真实查询键）、localStorage（真实键，
 * 由 `desktopStorageKey` 导入而不是重打）、URL。桥接会话由 `Desktop.tsx` 自己装（它本来
 * 就该装），这一页不替它装。
 */

/**
 * 等外链样式表真的进入级联。
 *
 * `desktop/bridge/accents.ts` 在**注册那一刻**用 getComputedStyle 取瓦片三件套，取早了
 * 就是空串，而注册发生在 `Desktop.tsx` 的 render 期——生产入口 `src/main.tsx` 没有这个
 * 等待，它靠的是「module 脚本被前行的 <link> 阻塞」这一条浏览器规则。夹具页不重复赌这条
 * 规则：这一页要测的是桌面装配，不该因为 CSS 先到还是 JS 先到而红。
 */
async function stylesheetsApplied(): Promise<void> {
  const links = [...document.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]')];
  await Promise.all(
    links.map(
      (link) =>
        link.sheet ||
        new Promise<void>((resolve) => {
          link.addEventListener("load", () => resolve(), { once: true });
          link.addEventListener("error", () => resolve(), { once: true });
        }),
    ),
  );
}

/**
 * 夹具自己的 QueryClient，不复用 `src/main.tsx` 的那个。
 *
 * 三个默认值都是为了让这一页「零请求」：`staleTime: Infinity` 让种下去的自定义应用清单
 * 永远不新鲜到需要重取；`retry: false` 让任何一条漏网的真实请求立刻定性为失败而不是在背
 * 后重试三次；`refetchOnWindowFocus: false` 是因为 Playwright 会切焦点，而一次焦点切换就
 * 重取一轮，测试看到的时序就不是人看到的时序。
 *
 * `useSession()` 自己钉了 60s 的 staleTime，比这里的更具体，因此以它为准——种下的会话在
 * 一条用例的几十秒内不会过期，这正是「首帧就有 ready 数据」要的形状。
 */
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: Infinity, refetchOnWindowFocus: false, retry: false },
    mutations: { retry: false },
  },
});

await stylesheetsApplied();

seedProdFixtureCache(queryClient);
writeStoredLayout(readProdLayoutChoice(window.location.search));

/**
 * 种键清单打在 console.warn 上，而不是打在 DOM 里。
 *
 * 这一页的桌面必须是**生产那一块桌面**：多一个调试节点，顶栏的「最右」判定与基线截图就
 * 多一个变量。warn 是 eslint 放行的唯一 console 出口，而这份耦合点最需要被看见的场合正是
 * 人盯着浏览器控制台的调试现场。
 */
console.warn(
  "[desktop-prod] seeded query keys:",
  JSON.stringify(SEEDED_QUERY_KEYS.map((key) => key.join("/"))),
);

const container = document.getElementById("root");
if (!container) {
  throw new Error("root container missing");
}

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <TooltipProvider delayDuration={300}>
          <Desktop />
          <AppToaster />
        </TooltipProvider>
      </SessionProvider>
    </QueryClientProvider>
  </StrictMode>,
);
