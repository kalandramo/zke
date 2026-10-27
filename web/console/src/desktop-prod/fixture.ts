import { APP_MANIFESTS } from "@/apps/registry";
import { queryKeys } from "@/api/query-keys";
import type {
  Capability,
  CustomApplication,
  CurrentSession,
  Permission,
  ScopeSelection,
} from "@/api/types";

import { desktopStorageKey } from "@/desktop/persistence";
import type { DesktopSnapshot } from "@kalandramo/desktop-kit";
import type { QueryClient } from "@tanstack/react-query";

/**
 * The fixture behind `desktop-prod.html`, which mounts the **real**
 * `src/desktop/Desktop.tsx`.
 *
 * This file exists because the production composition root had never been
 * executed by anything: the trial page is a same-shaped re-assembly of the same
 * bridge and the same kit components, but it is not `Desktop.tsx`, and every
 * claim about the restore gate, the deep link, the per-user runtime or the
 * dynamic-channel timing was therefore inference. Here the thing under test is
 * the thing that ships.
 *
 * The whole harness is three seams, and none of them is a hook added to
 * production code:
 *
 * 1. the TanStack Query cache, written under the Server's own query keys, so
 *    `useSession()` and `useCustomApplications()` find an answer on their first
 *    render and never reach the network;
 * 2. `localStorage`, written under `desktopStorageKey(userId)` — the real
 *    persistence key, imported rather than retyped, in kit's own snapshot shape;
 * 3. the URL, which `Desktop.tsx` already reads for deep links.
 *
 * Everything else — the bridge session, the scope hydration, the restore gate,
 * the autosave, the shortcut binding — is `Desktop.tsx` doing its own job.
 *
 * **Why the query keys are the fragile part.** A seeded key that stops matching
 * the key a hook asks for does not fail loudly: the query goes pending, the
 * desktop waits or shows an error view, and the test that was supposed to be
 * about window geometry starts failing for a reason nobody recognises. That is
 * the price of not adding a test hook, and it is the right price — the
 * alternative is production code that knows about its tests. The list below is
 * therefore the contract to re-check whenever `api/query-keys.ts` or a query
 * hook changes, and `desktop-prod.spec.ts` asserts that booting issues **zero**
 * `/api/v1/**` requests other than the event stream, which is the only
 * machine-visible proof that the seeds still land.
 */

/** Nil-UUID shaped and unmistakably fake, as in the trial fixture. */
const FIXTURE_USER_ID = "00000000-0000-0000-0000-0000000000d1";
const FIXTURE_TENANT_ID = "00000000-0000-0000-0000-0000000000a1";
const FIXTURE_PROJECT_ID = "00000000-0000-0000-0000-0000000000b2";
const CUSTOM_LOGO_APPLICATION_ID = "00000000-0000-0000-0000-0000000000c1";
const CUSTOM_PLAIN_APPLICATION_ID = "00000000-0000-0000-0000-0000000000c2";

/** The Unix epoch: nobody reads it as a live expiry. */
const FIXTURE_AT = "1970-01-01T00:00:00Z";

export const FIXTURE_SCOPE: ScopeSelection = {
  tenantId: FIXTURE_TENANT_ID,
  tenantName: "夹具租户",
  projectId: FIXTURE_PROJECT_ID,
  projectName: "夹具项目",
};

/** The two ids the desktop's dynamic area is built from. */
export const CUSTOM_LOGO_APP_ID = `custom-application:${FIXTURE_PROJECT_ID}:${CUSTOM_LOGO_APPLICATION_ID}`;
export const CUSTOM_PLAIN_APP_ID = `custom-application:${FIXTURE_PROJECT_ID}:${CUSTOM_PLAIN_APPLICATION_ID}`;

/**
 * Every permission the launcher asks about, read off the catalogue.
 *
 * Not a hand-written list: a stale admin fixture fails the way this project has
 * already been failed once — silently, with a tile missing.
 */
const CATALOGUE_PERMISSIONS: Permission[] = [
  ...new Set(APP_MANIFESTS.flatMap((manifest) => manifest.requiredPermissions)),
];

const CAPABILITIES: Capability[] = [
  {
    // `isGlobalAdmin` is a role test, not a permission test, so the tier that
    // must see all eleven tiles has to hold the role.
    role: "admin",
    scope_type: "global",
    permissions: CATALOGUE_PERMISSIONS,
  },
];

export const FIXTURE_SESSION: CurrentSession = {
  user: {
    id: FIXTURE_USER_ID,
    username: "fixture-admin",
    display_name: "夹具管理员",
  },
  expires_at: FIXTURE_AT,
  // Not a version number: the field is shown in 系统设置, and a plausible
  // `v0.3.1` would be a claim about a Server this page never contacted.
  server_version: "fixture",
  capabilities: CAPABILITIES,
  features: { aiops: true },
};

/**
 * An inline SVG logo as a data URL.
 *
 * A remote URL would 404 against `vite preview` and land on the icon's error
 * fallback, so the tile would prove the fallback rather than the logo path. A
 * data URL is the same `<img>` the Server's `logo_url` field carries in
 * production.
 */
function inlineLogo(mark: string, from: string, to: string): string {
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
    `<defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1">` +
    `<stop offset="0" stop-color="${from}"/><stop offset="1" stop-color="${to}"/>` +
    `</linearGradient></defs>` +
    `<rect width="32" height="32" rx="9" fill="url(#g)"/>` +
    `<text x="16" y="22" font-family="system-ui" font-size="15" font-weight="700" ` +
    `text-anchor="middle" fill="#fff">${mark}</text>` +
    `</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

function customApplication(
  id: string,
  name: string,
  url: string,
  logoURL: string,
  description: string,
): CustomApplication {
  return {
    id,
    project_id: FIXTURE_PROJECT_ID,
    name,
    description,
    url,
    logo_url: logoURL,
    created_at: FIXTURE_AT,
    updated_at: FIXTURE_AT,
  };
}

/**
 * Two applications, not one: the second has no logo, which is the branch where
 * the bridge must *not* hand the shell a face class.
 */
export const FIXTURE_CUSTOM_APPLICATIONS: CustomApplication[] = [
  customApplication(
    CUSTOM_LOGO_APPLICATION_ID,
    "夹具监控大盘",
    "https://grafana.example.com/d/zke-fixture/overview?orgId=1",
    inlineLogo("夹", "#f0a63a", "#b96a12"),
    "带 logo 的自定义应用：走宿主的浅色面。",
  ),
  customApplication(
    CUSTOM_PLAIN_APPLICATION_ID,
    "夹具无标应用",
    "https://plain.example.com/zke",
    "",
    "没有 logo 的自定义应用：走外壳自己的渐变面。",
  ),
];

/**
 * The seeded queries, in one place so the contract is listable.
 *
 * `staleTime` is not set here: `useSession()` pins its own 60s window and
 * `useCustomApplications()` inherits the harness client's `Infinity`, so neither
 * refetches under a test.
 */
export function seedProdFixtureCache(queryClient: QueryClient): void {
  queryClient.setQueryData(queryKeys.session(), FIXTURE_SESSION);
  queryClient.setQueryData(queryKeys.customApplications(FIXTURE_PROJECT_ID), {
    applications: FIXTURE_CUSTOM_APPLICATIONS,
  });
}

/** The keys above, spelled out for the report and for a human reading a failure. */
export const SEEDED_QUERY_KEYS: readonly (readonly unknown[])[] = [
  queryKeys.session(),
  queryKeys.customApplications(FIXTURE_PROJECT_ID),
];

/* ------------------------------------------------------------------ *
 * The stored layout
 * ------------------------------------------------------------------ */

/**
 * Which desktop the operator is presumed to have left behind.
 *
 * `blank` is the honest default: no windows, but a Project in the extensions
 * slot, because the Project is what makes the dynamic channel load at all.
 */
export type ProdLayoutChoice = "blank" | "custom-window" | "three-windows";

export function readProdLayoutChoice(search: string): ProdLayoutChoice {
  const value = new URLSearchParams(search).get("layout");
  return value === "custom-window" || value === "three-windows" ? value : "blank";
}

function windowEntry(
  id: string,
  appId: string,
  title: string,
  x: number,
  y: number,
  width = 520,
  height = 360,
): DesktopSnapshot["windows"][number] {
  const rect = { x, y, width, height };
  return { id, appId, title, rect, restoreRect: rect, mode: "normal" };
}

function snapshotWith(
  windows: DesktopSnapshot["windows"],
  focusedId: string | null,
): DesktopSnapshot {
  return {
    version: 1,
    windows,
    focusedId,
    dock: { pinned: [] },
    // The face the html bootstrap script has already painted: the snapshot must
    // not be the thing that changes the theme under a test.
    theme: { name: "light", preference: "system" },
    extensions: { scope: FIXTURE_SCOPE, dockVisible: true },
  };
}

function layoutFor(choice: ProdLayoutChoice): DesktopSnapshot {
  switch (choice) {
    case "custom-window":
      // The case the restore gate exists for: a window whose appId only the
      // dynamic channel can register. Without the wait, kit's knownAppIds guard
      // drops it and the desktop silently loses an application's window.
      return snapshotWith(
        [
          windowEntry("win-prod-custom", CUSTOM_LOGO_APP_ID, "夹具监控大盘", 96, 120, 640, 420),
          windowEntry("win-prod-resources", "resources", "组织与资源", 780, 140, 520, 380),
        ],
        "win-prod-custom",
      );
    case "three-windows":
      // Three, not two: a two-window stack cannot tell a walk from a toggle.
      return snapshotWith(
        [
          windowEntry("win-prod-a", "resources", "组织与资源", 60, 90),
          windowEntry("win-prod-b", "settings", "系统设置", 620, 90),
          windowEntry("win-prod-c", "platform", "平台配置", 340, 470),
        ],
        "win-prod-c",
      );
    case "blank":
      return snapshotWith([], null);
  }
}

/**
 * Write the layout under the real key before React mounts.
 *
 * `Desktop.tsx` reads it during its first render (the Dock's first paint must
 * not flicker), so anything later than "before render" would be a different
 * desktop than the one the test asked for.
 */
export function writeStoredLayout(choice: ProdLayoutChoice): void {
  try {
    localStorage.setItem(desktopStorageKey(FIXTURE_USER_ID), JSON.stringify(layoutFor(choice)));
  } catch {
    // A storage that refuses to write is a harness failure, not a product one;
    // the desktop then cold-starts and the spec's own assertions go red.
  }
}
