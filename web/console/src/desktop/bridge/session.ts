import type { CurrentSession } from "@/api/types";
import { EMPTY_PERMISSION_CHECKER, type PermissionChecker } from "@/auth/capabilities";

import type { ZkeBridgeDeps } from "./capabilities";

/**
 * The piece of the Console the bridge is allowed to see, reached from outside
 * React.
 *
 * Accessors rather than values, for the same reason `ZkeBridgeDeps` spells them
 * that way: registration happens before `<Desktop>` mounts, the registry freezes
 * at mount, and a session read at that moment is a session read from before
 * anybody answered. Kit re-runs the capability predicates on every render it
 * cares about, so the answer has to come from a call, not from a snapshot.
 */
export type BridgeSessionSource = {
  permissions: () => PermissionChecker;
  session: () => CurrentSession | null;
};

/**
 * The one module-global the bridge reads.
 *
 * It is a *seam*, not a store: `auth/SessionProvider` remains the only owner of
 * what the session is. Whoever installs a source is saying "when the registration
 * surface asks who is here, ask this", and the real entry will hand it the live
 * context once P5c wires the bridge into `main.tsx`.
 */
let source: BridgeSessionSource | null = null;

/**
 * Installs the session the bridge reads, or removes it.
 *
 * Passing `null` is not a convenience for tests: it is the logout path. A source
 * left installed after the operator signs out keeps answering permission gates
 * with the identity that just left, and the launcher would go on showing
 * applications the Server now refuses.
 */
export function setBridgeSession(next: BridgeSessionSource | null): void {
  source = next;
}

/**
 * The current permission checker, or one that grants nothing.
 *
 * The empty checker rather than a throw: a gate that cannot be answered must
 * close. `EMPTY_PERMISSION_CHECKER` is `createPermissionChecker([])`, so
 * `isGlobalAdmin` is false and every `canAnywhere` is false — the launcher shows
 * only the applications that name no permission, which is what an unauthenticated
 * desktop should show.
 */
export function readBridgePermissions(): PermissionChecker {
  return source?.permissions() ?? EMPTY_PERMISSION_CHECKER;
}

/** The current session, or `null` when nobody has installed one. */
export function readBridgeSession(): CurrentSession | null {
  return source?.session() ?? null;
}

/**
 * `ZkeBridgeDeps` wired to this module, ready to hand to `registerZkeApps`.
 *
 * Exported as a value rather than built at each call site because the two
 * readers must stay a pair. A caller that passed `readBridgePermissions` with an
 * ad-hoc `() => null` session would silently switch the deployment gate off for
 * AIOps, and nothing downstream would notice.
 */
export const bridgeSessionDeps: ZkeBridgeDeps = {
  permissions: readBridgePermissions,
  session: readBridgeSession,
};
