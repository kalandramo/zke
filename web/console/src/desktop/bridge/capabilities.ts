import type { AppManifest as ZkeAppManifest } from "@/apps/types";
import type { CurrentSession } from "@/api/types";
import type { PermissionChecker } from "@/auth/capabilities";
import type { AppManifest as KitAppManifest } from "@kalandramo/desktop-kit/registry";

/**
 * What the bridge needs in order to answer "may this entry be shown", supplied
 * from outside React.
 *
 * Accessors rather than values: registration happens before `<Desktop>` mounts,
 * which is before the session has been read, and the registry freezes at mount.
 * A snapshot taken there would keep answering for the rest of the session with
 * the answer from before anyone logged in. Kit re-evaluates the predicates at
 * every render it cares about, so reading through these is what lets the
 * launcher follow a role binding that changes later.
 */
export type ZkeBridgeDeps = {
  permissions: () => PermissionChecker;
  session: () => CurrentSession | null;
};

/**
 * The one predicate that reproduces the filter in `desktop/IconGrid.tsx`.
 *
 * Deliberately one predicate and not three. Kit composes `capabilities` with OR
 * — any predicate returning true shows the application — so splitting the role
 * gate, the deployment gate and the permission gate into three would put 平台配置
 * in front of anyone holding any permission at all, and AIOps on a deployment
 * with no model endpoint. The conjunction ZKE actually applies has to stay
 * inside a single predicate; breaking it apart is the one translation that is
 * not available.
 *
 * Like the filter it replaces this gates the entry, nothing more: the Server
 * re-authorizes every request, so a predicate that is wrong or bypassed yields
 * 403s rather than access.
 */
export function toCapabilityGates(
  zke: ZkeAppManifest,
  deps: ZkeBridgeDeps,
): KitAppManifest["capabilities"] {
  const gate = (): boolean => {
    const checker = deps.permissions();
    if (zke.requiresGlobalAdmin && !checker.isGlobalAdmin) {
      return false;
    }
    const feature = zke.requiresPlatformFeature;
    if (feature && !deps.session()?.features[feature]) {
      return false;
    }
    // A planned application is shown for exactly what it is, and an application
    // that names no permission is open to every signed-in operator — both of
    // which are the launcher's answers, not this bridge's to reconsider.
    if (zke.availability.state === "planned" || zke.requiredPermissions.length === 0) {
      return true;
    }
    return zke.requiredPermissions.some((permission) => checker.canAnywhere(permission));
  };

  return [gate];
}
