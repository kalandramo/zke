import type { OpenAppOptions, ShellRuntime } from "@kalandramo/desktop-kit";

import { useScopeStore } from "@/scope/scope-store";

/**
 * The shell as the rest of the Console reaches it: from outside React, and from
 * modules that must not know a kit runtime exists.
 *
 * Two consumers need exactly this. `apps/evidence-link.ts` opens a window from a
 * click inside a conversation, with no runtime in scope; and the applications that
 * still read window state through `desktop/window-store` need the store behind
 * whatever desktop is currently on screen. Both used to reach a module-level
 * zustand store that was the whole truth; now the truth lives in the runtime the
 * authenticated component created, so this seam holds the pointer and nothing
 * else.
 *
 * It is a handle, not a second store: installing a runtime copies no state, and a
 * new operator gets a new runtime rather than the previous one's windows.
 */
let activeShell: ShellRuntime | null = null;

/**
 * The host-owned half of the desktop: what the layout key carries that is nobody's
 * window state. Kit hands that slot to the host and never reads it (see
 * `DesktopSnapshot.extensions`), which is why the Dock flag lives here beside the
 * Project selection rather than in the window store.
 */
const hostState = { dockVisible: true };

export function installShell(runtime: ShellRuntime): void {
  activeShell = runtime;
}

/**
 * Clears the handle, but only the one it holds.
 *
 * React runs the cleanup of the previous mount after the newer mount has already
 * installed its own runtime; an unconditional clear would drop a live desktop.
 */
export function uninstallShell(runtime: ShellRuntime): void {
  if (activeShell === runtime) {
    activeShell = null;
  }
}

export function getShell(): ShellRuntime | null {
  return activeShell;
}

export function setDockVisiblePreference(visible: boolean): void {
  hostState.dockVisible = visible;
}

/**
 * What `runtime.persist()` writes into the snapshot's extension slot.
 *
 * Read from the stores rather than passed in, because persist() fires from
 * kit's autosave as well as from the host, and only the stores know what the
 * operator selected at that moment.
 */
export function readHostExtensions(): Record<string, unknown> {
  return { scope: useScopeStore.getState().scope, dockVisible: hostState.dockVisible };
}

/**
 * Opens an application window on the desktop the operator is looking at.
 *
 * Returns the new window's id, or `""` when nothing was opened: no desktop on
 * screen, the application is not registered, or it is planned rather than built.
 * Kit refuses rather than throws here, so callers check for the empty string —
 * a `try` around this would catch nothing and would hide the refusal.
 */
export function openWindow(
  appId: string,
  args?: Record<string, unknown>,
  options?: OpenAppOptions,
): string {
  return activeShell?.openApp(appId, args, options) ?? "";
}
