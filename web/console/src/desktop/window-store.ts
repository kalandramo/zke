import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

import type { OpenAppOptions, WindowInstance, WindowStoreState } from "@kalandramo/desktop-kit";

import { getShell, openWindow } from "./shell";

export type { WindowInstance };

/**
 * The window state as the applications were written to see it.
 *
 * The store itself moved into the kit runtime, which keeps its actions on the
 * store *object* rather than in its state — and four applications select
 * `state.closeAll` or `state.windows[id].mode` through this module. They are
 * outside the scope of a shell swap, so the shape they were written against is
 * what this hands back: the runtime's state, plus the two actions that used to
 * live beside it.
 */
export type WindowStoreView = WindowStoreState & {
  closeAll: () => void;
  openWindow: (appId: string, args?: Record<string, unknown>, options?: OpenAppOptions) => string;
};

/**
 * A desktop with nothing on it, for a surface that has no shell.
 *
 * `desktop-trial` renders the real applications through kit without ever
 * installing a module-level runtime, and the kit-lab page renders components
 * outside any desktop at all. An application asking whether its own window is
 * focused deserves `false` there, not an exception in the middle of a page whose
 * subject is something else entirely.
 */
const emptyStore = createStore<WindowStoreState>(() => ({
  windows: {},
  order: [],
  focusedId: null,
  desktopRevealed: false,
  zCounter: { normal: 0, top: 0 },
}));

// Stable identities: an application that selects `state.closeAll` must not see a
// new function on every store write, or the selector result changes and the
// component re-renders for nothing.
const closeAll = (): void => {
  getShell()?.closeAll();
};

function viewOf(state: WindowStoreState): WindowStoreView {
  return { ...state, closeAll, openWindow };
}

function currentStore() {
  return getShell()?.store ?? emptyStore;
}

export function useWindowStore<T>(selector: (state: WindowStoreView) => T): T {
  return useStore(currentStore(), (state) => selector(viewOf(state)));
}

useWindowStore.getState = (): WindowStoreView => viewOf(currentStore().getState());
