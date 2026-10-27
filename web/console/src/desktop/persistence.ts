import type { DesktopSnapshot, PersistenceAdapter } from "@kalandramo/desktop-kit";

import { EMPTY_SCOPE, type ScopeSelection } from "@/api/types";

/**
 * Where the desktop layout lives, and what it looks like now.
 *
 * One key per operator, unchanged since the shell was written by hand: an
 * operator who reloads the Console must not lose their windows because the shell
 * changed vendors. What lives *under* the key did change — the pre-kit store
 * serialised `{desktop, scope, dockVisible}` and kit serialises a
 * `DesktopSnapshot` — so `load()` recognises both shapes and translates the old
 * one on the way in. The translation is one-directional and never written back
 * until the desktop changes for a real reason, which means a layout that is read
 * and re-read without being touched keeps working exactly as it did.
 */
export function desktopStorageKey(userId: string): string {
  return `zke.desktop.layout.${userId}`;
}

/** The desktop's own state: everything the layout key carries that is nobody's window. */
export type DesktopHostState = {
  scope: ScopeSelection;
  dockVisible: boolean;
};

export type StoredDesktopLayout = {
  snapshot: DesktopSnapshot;
  host: DesktopHostState;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hostStateOf(scope: unknown, dockVisible: unknown): DesktopHostState {
  return {
    scope: { ...EMPTY_SCOPE, ...(isRecord(scope) ? scope : {}) } as ScopeSelection,
    // Layouts written before the Dock could be hidden have no flag; shown is the
    // state they were saved in.
    dockVisible: dockVisible !== false,
  };
}

/**
 * The theme the browser already applied, stamped into the migrated snapshot.
 *
 * The snapshot carries a *copy* of the theme; `index.html` and `zke.theme` are the
 * truth, and they were resolved before this module ran. A migration that stamped
 * the kit default here would repaint a dark desktop light on the next reload —
 * which is a silent behaviour change, not a migration.
 */
function appliedTheme(): DesktopSnapshot["theme"] {
  const name = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
  return { name, preference: name };
}

function readStored(key: string): unknown {
  try {
    const raw = localStorage.getItem(key);
    return raw === null ? null : JSON.parse(raw);
  } catch {
    // Unparseable, or storage unavailable: not a layout. The desktop starts cold
    // rather than guessing at a half-read one.
    return null;
  }
}

/**
 * Reads the layout under this operator's key in either format.
 *
 * Returns `null` for no layout, a corrupt one, or one that is neither shape — all
 * three mean "start with an empty desktop", which is what the hand-written shell
 * did with the same three cases.
 */
export function readDesktopLayout(userId: string): StoredDesktopLayout | null {
  const parsed = readStored(desktopStorageKey(userId));
  if (!isRecord(parsed)) {
    return null;
  }

  // The kit shape: version and windows at the top level, host state in the
  // extension slot. Passed through untouched — core validates each window on the
  // way into the store and drops the ones that do not hold up.
  if (parsed.version === 1 && Array.isArray(parsed.windows)) {
    const snapshot = parsed as unknown as DesktopSnapshot;
    const extensions = isRecord(snapshot.extensions) ? snapshot.extensions : {};
    return {
      snapshot,
      host: hostStateOf(extensions.scope, extensions.dockVisible),
    };
  }

  // The shape the pre-kit store wrote: the windows under a `desktop` key, with the
  // scope and the Dock flag beside it rather than inside it.
  const desktop = parsed.desktop;
  if (isRecord(desktop) && desktop.version === 1 && Array.isArray(desktop.windows)) {
    const host = hostStateOf(parsed.scope, parsed.dockVisible);
    return {
      host,
      snapshot: {
        version: 1,
        windows: desktop.windows as DesktopSnapshot["windows"],
        focusedId: typeof desktop.focusedId === "string" ? desktop.focusedId : null,
        // Nothing was ever pinned in the hand-written Dock; every registered
        // application had a tile. An empty pin list reads as "no opinions".
        dock: { pinned: [] },
        theme: appliedTheme(),
        extensions: { scope: host.scope, dockVisible: host.dockVisible },
      },
    };
  }

  return null;
}

/** The application ids the stored layout would restore, for the boot-order check in `Desktop.tsx`. */
export function readStoredWindowAppIds(userId: string): string[] {
  const layout = readDesktopLayout(userId);
  if (!layout) {
    return [];
  }
  return layout.snapshot.windows
    .filter((window) => isRecord(window) && typeof window.appId === "string")
    .map((window) => window.appId as string);
}

/**
 * The adapter kit drives persistence with.
 *
 * `load()` is where the one-time migration happens, so `restore()` never sees the
 * old shape and `migrate` stays unused — the alternative, teaching core about a
 * shape only this product ever wrote, would put ZKE's history in a published
 * package. `save()` writes the snapshot verbatim: the shape on disk from here on
 * is kit's, and the next read takes the first branch above.
 */
export function createZkePersistenceAdapter(userId: string): PersistenceAdapter {
  const key = desktopStorageKey(userId);
  return {
    load: () => Promise.resolve(readDesktopLayout(userId)?.snapshot ?? null),
    save: (snapshot) => {
      try {
        localStorage.setItem(key, JSON.stringify(snapshot));
      } catch {
        // Storage may be unavailable or full; the desktop keeps working in memory.
      }
      return Promise.resolve();
    },
  };
}

/**
 * Drops the layout for this operator.
 *
 * Kit exposes no clear API — the adapter is the only thing that knows where state
 * is kept — so 重置桌面布局 deletes the key here, exactly as the hand-written
 * shell did. The desktop that follows writes a fresh one as soon as the operator
 * opens a window.
 */
export function clearDesktopState(userId: string): void {
  try {
    localStorage.removeItem(desktopStorageKey(userId));
  } catch {
    // Ignored for the same reason as above.
  }
}
