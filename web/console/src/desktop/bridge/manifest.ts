import type { AppManifest as ZkeAppManifest } from "@/apps/types";
import type {
  AppManifest as KitAppManifest,
  DockItem as KitDockItem,
} from "@kalandramo/desktop-kit/registry";

import { createAppEntry } from "./app-adapter";
import { toCapabilityGates, type ZkeBridgeDeps } from "./capabilities";

/**
 * One ZKE application, in the shell's own format.
 *
 * `deps` is a second parameter rather than the single argument the plan named,
 * and it cannot be otherwise: the capability gate has to ask the current session
 * something, and the only non-React way to hand it that answer is to be given
 * the source. A one-argument version would either read a module-global session —
 * a second, drift-prone copy of the truth — or return a manifest with no gate at
 * all, which is the wider and worse of the two failures.
 *
 * `singleton` is left unset on purpose. Kit's default is one window per
 * application, which is already the rule `desktop/window-store.ts` enforces, so
 * restating it here would be a second place to change the answer.
 *
 * `keepInDock: false` is stated explicitly because kit's default is the mac
 * behaviour — an icon that stays in the Dock after its window closes — and ZKE's
 * Dock is a list of what is running, not a list of what exists. The launcher is
 * already the eleven-application list; a Dock that repeated it would push the
 * running applications apart with nine tiles that do nothing, and the one thing
 * the Dock is for is coming back to a minimized terminal.
 *
 * `plannedBehavior: "open"` is the one field added beyond the plan's list. Kit's
 * default refuses to open a planned application at all, while ZKE's launcher
 * deliberately makes every planned tile pressable onto `planned/PlannedApp`,
 * which states its phase and its planned capabilities and shows no data. Leaving
 * the default in place would trade a truthful placeholder window for a click that
 * does nothing, so the field that says "the shell is ready and the content is
 * staged" is set for the applications that are staged. Nothing in the catalogue
 * is planned today, so this is inert until the next capability is announced
 * before it is built.
 */
export function toKitAppManifest(zke: ZkeAppManifest, deps: ZkeBridgeDeps): KitAppManifest {
  return {
    id: zke.id,
    title: zke.title,
    description: zke.description,
    icon: zke.icon,
    accent: zke.accent,
    capabilities: toCapabilityGates(zke, deps),
    availability: zke.availability,
    defaultSize: zke.defaultSize,
    keepInDock: false,
    ...(zke.availability.state === "planned" ? { plannedBehavior: "open" } : null),
    mount: "react",
    entry: createAppEntry(zke),
  };
}

/**
 * The Dock tile for one ZKE application.
 *
 * `id` is namespaced because it lives in the registry's Dock namespace, which
 * also holds host actions — 显示桌面 is a Dock item too, and an id collision there
 * is silently resolved by dropping the second registration.
 *
 * No `order`: kit sorts by `order` with a stable tie-break on registration
 * sequence, so leaving it unset keeps the Dock in catalogue order, which is the
 * order the launcher already uses and the one 容器服务 and Helm 应用 were placed
 * next to each other in.
 *
 * The tile is registered for applications the operator cannot currently see as
 * well. Kit hides a Dock tile whose application fails its capability gate and
 * warns about it, so the answer is recomputed per render against the live
 * session; a Dock item filtered at registration time would be a decision made
 * before anyone had logged in.
 */
export function toDockItem(zke: ZkeAppManifest): KitDockItem {
  return {
    id: `dock-${zke.id}`,
    appId: zke.id,
    title: zke.title,
    icon: zke.icon,
    accent: zke.accent,
  };
}
