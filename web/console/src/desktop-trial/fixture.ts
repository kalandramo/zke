import { APP_MANIFESTS } from "@/apps/registry";
import {
  EMPTY_SCOPE,
  type Capability,
  type CustomApplication,
  type CurrentSession,
  type Permission,
  type ScopeSelection,
} from "@/api/types";
import { createPermissionChecker, type PermissionChecker } from "@/auth/capabilities";

/**
 * The two identities the trial runs under.
 *
 * They are a fixture, not a login: nothing here talks to the Server, so the
 * launcher's answers have to be manufactured. Two tiers because one proves the
 * shell can draw eleven tiles and the other proves the shell can *filter* them —
 * which is the only way to see that the bridge's capability gate is live rather
 * than decorative.
 */
export type TrialRole = "admin" | "member";

/**
 * Whether the trial seeds a Project scope.
 *
 * Seven of the eleven applications open on `ScopeRequired` unless
 * `scope.projectId` is set — they read it straight out of the module-level
 * `useScopeStore`, with no Provider and no top bar in the path. Seeded is the
 * default so that the trial reaches those applications' real views; `none` is
 * kept as a query switch so the guard itself stays observable rather than being
 * assumed away.
 */
export type TrialScopeChoice = "seeded" | "none";

export type TrialFixture = {
  role: TrialRole;
  session: CurrentSession;
  permissions: PermissionChecker;
  scope: ScopeSelection;
  /** 首帧挂在哪一组自定义应用上；null 表示动态通道整条不装。 */
  customGroup: TrialCustomAppGroup | null;
};

/**
 * A nil UUID, chosen because it is unmistakably not a real account.
 *
 * 系统设置 renders `session.user`, so the fixture has to carry something; an
 * id that looked like a real one would be indistinguishable from a leaked
 * identity in a screenshot.
 */
const FIXTURE_USER_ID = "00000000-0000-0000-0000-000000000000";

/** The Unix epoch for the same reason: nobody reads it as a live expiry. */
const FIXTURE_EXPIRES_AT = "1970-01-01T00:00:00Z";

/**
 * The Project the seeded tier works in.
 *
 * Same rule as the user id: unmistakably fake. The ids are nil-UUID shaped
 * because they are passed straight into query keys and, on a real desktop, into
 * `/api/v1/...` path segments — a value like `"fake"` would survive, but a value
 * that a validator could reject would make the trial fail for a reason that has
 * nothing to do with the shell. The names are Chinese and say 夹具 because they
 * are what the applications echo back into their own headers and scope lines, and
 * a screenshot has to be readable as a fixture on its own.
 *
 * The shape matches the deep-link write in `desktop/Desktop.tsx`
 * (`{ tenantId, tenantName, projectId, projectName }`), and a Tenant is always
 * paired with its Project: `ScopeSelection` documents that a Project implies its
 * Tenant and the two are never set apart.
 */
const PROJECT_MONITORING_ID = "00000000-0000-0000-0000-0000000000b2";

const FIXTURE_SCOPE: ScopeSelection = {
  tenantId: "00000000-0000-0000-0000-0000000000a1",
  tenantName: "夹具租户",
  projectId: PROJECT_MONITORING_ID,
  projectName: "夹具项目",
};

/**
 * Every permission the launcher actually asks about.
 *
 * Read off `APP_MANIFESTS` rather than copied from the OpenAPI enum: the trial
 * is testing the gate, and the gate only ever consults `requiredPermissions`.
 * A hand-copied list of all 44 permissions would go stale the first time the
 * Server adds one, and a stale admin fixture fails the way this project has
 * already been failed once — silently, with a tile missing.
 */
const CATALOGUE_PERMISSIONS: Permission[] = [
  ...new Set(APP_MANIFESTS.flatMap((manifest) => manifest.requiredPermissions)),
];

/**
 * The Server's builtin `viewer` grant, verbatim from `pkg/server/rbac/roles.go`.
 *
 * Not widened: the member tier is only required to drop 平台配置 and AIOps, and
 * it earns that by being a real read-only role rather than by switching
 * permissions off one at a time until the two tiles vanish. A fixture invented
 * to match the assertion would prove nothing about the assertion.
 */
const VIEWER_PERMISSIONS: Permission[] = ["tenant.read", "project.read", "cluster.read"];

function capabilitiesFor(role: TrialRole): Capability[] {
  if (role === "admin") {
    // `isGlobalAdmin` is a role test, not a permission test — the Server reserves
    // granting `admin` to the people already holding it, so a fixture that handed
    // out every permission would still be refused 平台配置.
    return [
      {
        role: "admin",
        scope_type: "global",
        permissions: CATALOGUE_PERMISSIONS,
      },
    ];
  }
  return [
    {
      role: "viewer",
      scope_type: "global",
      permissions: VIEWER_PERMISSIONS,
    },
  ];
}

function sessionFor(role: TrialRole): CurrentSession {
  return {
    user: {
      id: FIXTURE_USER_ID,
      username: `fixture-${role}`,
      display_name: role === "admin" ? "夹具管理员" : "夹具只读用户",
    },
    expires_at: FIXTURE_EXPIRES_AT,
    // Not a version number: the field is shown in 系统设置, and a plausible
    // `v0.3.1` would be a claim about a Server this page never contacted.
    server_version: "fixture",
    capabilities: capabilitiesFor(role),
    // A deployment fact, not an identity fact: both tiers describe the same
    // install, so AIOps leaves the member tier on its permission (`ai.run`), not
    // on the feature flag. The flag's false branch is therefore untested here.
    features: { aiops: true },
  };
}

/**
 * The role from `?role=`, defaulting to admin.
 *
 * Anything unrecognised falls back to admin rather than throwing: the page is a
 * trial harness, and a typo in a query string should show somebody the desktop
 * with the wrong tiles, not a blank screen that says nothing about which tiles
 * were supposed to be there.
 */
export function readTrialRole(search: string): TrialRole {
  const params = new URLSearchParams(search);
  return params.get("role") === "member" ? "member" : "admin";
}

/**
 * The scope tier from `?scope=`, defaulting to seeded.
 *
 * Only `none` opts out, so a typo lands on the seeded desktop rather than on a
 * page where seven applications quietly show their guard — the same fallback rule
 * `readTrialRole` uses, for the same reason.
 */
export function readTrialScopeChoice(search: string): TrialScopeChoice {
  return new URLSearchParams(search).get("scope") === "none" ? "none" : "seeded";
}

export function createTrialFixture(
  role: TrialRole,
  scopeChoice: TrialScopeChoice = "seeded",
  customChoice: TrialCustomChoice = "off",
): TrialFixture {
  const customGroup = customChoice === "off" ? null : trialCustomAppGroup(customChoice);
  return {
    role,
    session: sessionFor(role),
    // The real checker, not a stub: the member tier's whole point is that
    // `createPermissionChecker` is what filters 平台配置 and AIOps out.
    permissions: createPermissionChecker(capabilitiesFor(role)),
    /*
     * 装了组 B 就得同时把作用域换成 B：自定义应用是按项目列的，顶栏写着 A、桌面上
     * 挂着 B 的项目清单，这一页就成了自相矛盾的样本。未选择项目（?scope=none）优先，
     * 那条验收要的是门控本身。
     */
    scope: scopeChoice === "none" ? EMPTY_SCOPE : (customGroup?.scope ?? FIXTURE_SCOPE),
    customGroup,
  };
}

/* ------------------------------------------------------------------ *
 * 自定义应用夹具（kit 0.8.0 动态通道）
 * ------------------------------------------------------------------ */

/**
 * One Project's custom applications, as a group the dynamic channel can publish.
 *
 * Two groups exist because the thing under test is the *swap*, not the list:
 * `setDynamicApps` replaces the whole dynamic area in one call, so proving it
 * needs a second area to swap to. Group A is the seeded Project — the one
 * `createTrialFixture` already hydrates — so the trial boots with its own
 * applications on the desktop, which is the real product shape (you are always
 * in *some* Project). Group B is a different Project with a different set, and
 * switching to it is what makes the orphan-window duty observable.
 */
export type TrialCustomAppGroup = {
  key: "monitoring" | "logging";
  /** 顶栏上给操作者看的那一行；也是测试断言「现在挂着哪一组」的锚点。 */
  label: string;
  scope: ScopeSelection;
  applications: CustomApplication[];
};

/**
 * An inline SVG logo as a data URL.
 *
 * Not a remote URL: the trial runs against `vite preview` with no Server and no
 * network, and a logo that 404s would land on the `onError` fallback and make
 * 「瓦片里是 img」 fail for a reason that has nothing to do with the bridge. A
 * data URL is the same `<img>` path the Server's `logo_url` field takes (it
 * accepts Base64 data URLs), so nothing about the render is special-cased.
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
  projectId: string,
  id: string,
  name: string,
  url: string,
  logoURL: string,
  description: string,
): CustomApplication {
  return {
    id,
    project_id: projectId,
    name,
    description,
    url,
    logo_url: logoURL,
    created_at: FIXTURE_EXPIRES_AT,
    updated_at: FIXTURE_EXPIRES_AT,
  };
}

/** 组 B 的项目：与 `FIXTURE_SCOPE` 同租户、不同项目。 */
const PROJECT_LOGGING_ID = "00000000-0000-0000-0000-0000000000b4";

const PROJECT_LOGGING: ScopeSelection = {
  tenantId: "00000000-0000-0000-0000-0000000000a1",
  tenantName: "夹具租户",
  projectId: PROJECT_LOGGING_ID,
  projectName: "夹具项目·日志",
};

export const TRIAL_CUSTOM_APP_GROUPS: readonly [TrialCustomAppGroup, TrialCustomAppGroup] = [
  {
    key: "monitoring",
    label: "监控项目",
    scope: FIXTURE_SCOPE,
    applications: [
      customApplication(
        PROJECT_MONITORING_ID,
        "00000000-0000-0000-0000-0000000000c1",
        "监控大盘",
        "https://grafana.example.com/d/zke-overview/monitoring?orgId=1",
        inlineLogo("监", "#f0a63a", "#b96a12"),
        "Grafana 大盘：集群与业务指标的统一视图。",
      ),
      customApplication(
        PROJECT_MONITORING_ID,
        "00000000-0000-0000-0000-0000000000c2",
        "Grafana Explore",
        "https://grafana.example.com/explore?orgId=1",
        inlineLogo("G", "#e0724a", "#9c3c1c"),
        "临时查询与指标探索入口。",
      ),
      /*
       * 第三条刻意没有 logo。`logo_url` 是服务端字段，空串是它的正常取值之一（创建时可以不
       * 填），而空串走的是另一条面：桥接因此不传 `faceClass`，瓦片留在外壳自己的渐变面上。
       * 组里没有这一条，动态通道就只验过「有 logo」这一半，而「两处同底」的另一半——无 logo
       * 时不许把宿主的渐变类串盖到外壳的渐变面之上——没人看着。
       */
      customApplication(
        PROJECT_MONITORING_ID,
        "00000000-0000-0000-0000-0000000000c4",
        "CI 看板",
        "https://jenkins.example.com/view/zke",
        "",
        "自建 Jenkins 视图入口（无 logo）。",
      ),
    ],
  },
  {
    key: "logging",
    label: "日志项目",
    scope: PROJECT_LOGGING,
    applications: [
      customApplication(
        PROJECT_LOGGING_ID,
        "00000000-0000-0000-0000-0000000000c3",
        "日志平台",
        "https://logs.example.com/search?project=zke",
        inlineLogo("日", "#5aa9d6", "#2b6b96"),
        "集中日志检索与告警上下文。",
      ),
    ],
  },
];

/**
 * 首帧装载哪一组，来自 `?custom=`，缺省 `off`。
 *
 * 缺省必须是 off，理由不在页面上而在验收线上：既有的「11 个瓦片都在」和 member 那条
 * 用例数的是启动器的 `li`，动态组一装就是 13 个。那 8 条是换装前的基线，它们必须原样
 * 绿，所以动态通道由新用例显式开起来，而不是由首帧默认开起来。
 */
export type TrialCustomChoice = TrialCustomAppGroup["key"] | "off";

export function readTrialCustomChoice(search: string): TrialCustomChoice {
  const value = new URLSearchParams(search).get("custom");
  return value === "monitoring" || value === "logging" ? value : "off";
}

/** 下一组：两组之间来回切，所以这是一个环而不是一个序列。 */
export function nextTrialCustomAppGroup(current: TrialCustomAppGroup["key"]): TrialCustomAppGroup {
  const [first, second] = TRIAL_CUSTOM_APP_GROUPS;
  return current === first.key ? second : first;
}

/** 按 key 取组；测试与入口都要用同一个查找，不要各写一份 find。 */
export function trialCustomAppGroup(key: TrialCustomAppGroup["key"]): TrialCustomAppGroup {
  return TRIAL_CUSTOM_APP_GROUPS.find((group) => group.key === key) ?? TRIAL_CUSTOM_APP_GROUPS[0];
}
