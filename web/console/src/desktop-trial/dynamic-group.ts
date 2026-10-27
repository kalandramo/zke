import { lazy } from "react";

import type { ShellRuntime } from "@kalandramo/desktop-kit";

import { buildCustomApplicationGroup, closeOrphanWindows } from "@/desktop/bridge";
import { bridgeSessionDeps } from "@/desktop/bridge/session";

import type { TrialCustomAppGroup } from "./fixture";
import { TrialCustomAppContent } from "./trial-custom-app";

/**
 * 交给桥接 `entryFor` 的那一层 `lazy`。
 *
 * 包一层是为了和生产清单同一个形状：`AppManifest["entry"]` 就是
 * `LazyExoticComponent`，桥接层不该因为「这是试跑页」而接受另一种类型。
 */
const trialCustomAppEntry = lazy(async () => ({ default: TrialCustomAppContent }));

/**
 * 把一组自定义应用发布到动态通道，并关掉因此变成孤儿的窗口；返回关掉了几扇。
 *
 * 顺序是有讲究的，而且只有这一处能体现：`closeOrphanWindows` 读的是注册表的**合并视图**，
 * 所以它必须排在 `setDynamicApps` 之后。先关后发会拿旧清单当事实——刚发布那一组里恰好
 * 开着的窗口被当成合法留下，而该留的静态应用窗口反倒成了孤儿被关掉。
 *
 * `entryFor` 只换内容层：生产上这一格是 `CustomApplicationFrame`（指向第三方源的
 * iframe），试跑页要的是「看得见自己有没有被重新挂载」，所以换成一块本地面板。
 * 桥接、适配器、能力门控、图标工厂与标题栏槽走的都还是生产那一条路径。
 */
export function publishTrialCustomGroup(runtime: ShellRuntime, group: TrialCustomAppGroup): number {
  runtime.registry.setDynamicApps(
    buildCustomApplicationGroup(group.applications, bridgeSessionDeps, {
      entryFor: () => trialCustomAppEntry,
    }),
  );
  return closeOrphanWindows(runtime).length;
}
