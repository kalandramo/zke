import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// desktop-kit 的唯一 CSS 出口，与 src/main.tsx 同一份；瓦片令牌在它之后。
import "@kalandramo/desktop-kit/styles.css";
import "@/styles/app-accents.css";

import { createShellRuntime } from "@kalandramo/desktop-kit";
import { createRegistry } from "@kalandramo/desktop-kit/registry";

import { registerZkeApps } from "@/desktop/bridge";
import { bridgeSessionDeps, setBridgeSession } from "@/desktop/bridge/session";
import { useScopeStore } from "@/scope/scope-store";
import { publishTrialCustomGroup } from "./dynamic-group";
import {
  createTrialFixture,
  readTrialCustomChoice,
  readTrialRole,
  readTrialScopeChoice,
} from "./fixture";
import { TrialShell } from "./trial-shell";

/**
 * 等外链样式表真的进入级联。
 *
 * `desktop/bridge/accents.ts` 在**注册那一刻**用 getComputedStyle 把三件套从级联里取
 * 回来——它不复制色值，所以取早了就是空串。构建产物里 CSS 是 `<link rel=stylesheet>`，
 * 与 module 脚本并行取；注册又必须先于 `createRoot().render()`（kit 挂载即冻结注册表），
 * 两头之间没有天然的先后顺序，只能显式等一次。
 *
 * 读 `link.sheet` 而不是 `document.styleSheets`：加载失败的表 `sheet` 恒为 null，
 * 那种情况下继续注册拿到的是空串，而 accents.ts 会逐条 warn——比卡死在这页好。
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
 * 装配顺序，整页只有一条规则：夹具先装，注册再跑，两者都在 render 之前。
 *
 * kit 的注册表挂载即冻结，冻结之后所有 `register*` 只是一句 console.warn；而注册表冻结
 * 之前，能力门控得先有「谁在这儿」可问。接缝写成访问器正是为了这一点——见
 * `desktop/bridge/session.ts`。
 */
const search = window.location.search;
const fixture = createTrialFixture(
  readTrialRole(search),
  readTrialScopeChoice(search),
  readTrialCustomChoice(search),
);

setBridgeSession({
  permissions: () => fixture.permissions,
  session: () => fixture.session,
});

/**
 * 种作用域，和种身份一样在 render 之前。
 *
 * `useScopeStore` 是模块级 Zustand store，不在任何 Provider 下面——应用是直接
 * `useScopeStore((state) => state.scope)` 读的。所以「先选择项目再进应用」这条路径
 * 不需要顶栏参与，也不需要改任何应用代码；上一版把 7 个应用停在 `ScopeRequired` 归因给
 * kit 顶栏没有槽位，是归错了：这里没放过值，才是那 7 个应用进不去的原因。
 *
 * 用 `hydrate` 而不是 `setScope`：两者当前实现相同，但 `setScope` 在真实入口里代表
 * 「操作者亲手选了一个项目」，而这一页没有人选过任何东西，语义上它是一次恢复。
 */
useScopeStore.getState().hydrate(fixture.scope);

/**
 * 隔离注册表，而不是 `defaultRegistry`。
 *
 * 这一页要回答的是「这 11 个应用挂到 kit 外壳上是什么样」，而会话默认注册表是全模块共享
 * 的：同一份 bundle 里一旦出现第二个桌面表面，两边就往同一个注册表里写，冻结时机也变得
 * 互相依赖。自己的 registry 让 `trial-counts` 只由这一页决定。
 */
/*
 * `cascade: true`：ZKE 的新窗口从「剩余宽度的 1/6」起、每多开一扇向右下叠 26px
 * （kit 的 cascadeRect：desktop-kit core 的 geometry.ts，step 26、每 6 扇回环；用户看到的
 * 「窗口偏左」就是它）。kit 的缺省是居中，不开这个开关，换装后第一扇窗口会跑到屏幕正中——用户一眼就觉得
 * 桌面变了样。
 */
const runtime = createShellRuntime({ registry: createRegistry(), cascade: true });

/**
 * 不给 `adapter`，持久化因此整条关掉。
 *
 * 换装实验要的是可重复的首帧：带 localStorage 的桌面每次刷新都恢复上一轮的窗口，于是
 * 「11 个瓦片都在」测的是注册表，截图测的却是某个历史遗留状态。真实入口接的是
 * `desktop/persistence` 那份按用户分键的存储，不在这页的验证范围内。
 */
await stylesheetsApplied();
const counts = registerZkeApps(runtime.registry, bridgeSessionDeps);

/**
 * 装上一组的自定义应用，走 kit 0.8.0 的动态通道。
 *
 * 放在 `registerZkeApps` 之后、`render` 之前，和顶栏那两个按钮用的是同一个函数
 * （`dynamic-group.ts`）——首帧与点击之后必须是同一条代码路径，否则这一页证明的只是
 * 「第一次装配没问题」，而生产上动态清单永远是在一个已经跑起来的桌面上被换掉的。
 *
 * 静态注册先跑完，动态组才有东西可「不冲突」：`setDynamicApps` 的合并顺序是静态在前、
 * 动态在后，自定义应用的 id 形如 `custom-application:<项目>:<应用>`，与那 11 个静态 id
 * 天然不同，所以不会被注册表的同名丢弃规则吃掉。
 */
if (fixture.customGroup) {
  publishTrialCustomGroup(runtime, fixture.customGroup);
}

const container = document.getElementById("root");
if (!container) {
  throw new Error("root container missing");
}

createRoot(container).render(
  <StrictMode>
    <TrialShell fixture={fixture} counts={counts} runtime={runtime} />
  </StrictMode>,
);
