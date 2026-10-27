import { getRegistryState, mergedApps } from "@kalandramo/desktop-kit/registry";
import type { ShellRuntime } from "@kalandramo/desktop-kit";

import type { CustomApplication } from "@/api/types";
import { customApplicationProjectId } from "@/apps/registry";

import { buildCustomApplicationGroup } from "./custom-apps";
import { bridgeSessionDeps } from "./session";

/**
 * Closes the windows left behind by a dynamic-area swap.
 *
 * Kit deliberately does not do this. `knownAppIds` is a *restore* guard: an
 * application that has disappeared from the merged list loses its window the
 * next time the desktop is rebuilt from a snapshot, and nothing before that
 * moment. A live desktop therefore keeps showing a window whose manifest is
 * already gone — the content stays mounted, the title bar still reads like the
 * application exists, and the operator has no signal that the thing they are
 * looking at belongs to a Project they just left.
 *
 * That gap is the host's, because only the host knows why the list changed. So
 * the rule lives here, next to the bridge that produces the list, and it reads
 * the **merged** view rather than the group it just published: the static
 * catalogue is always in that list, so a window belonging to one of the eleven
 * is never a candidate for closing. Forgetting the static half is the one way to
 * write this function, and it would close the entire desktop on every project
 * switch.
 *
 * `runtime.closeWindow` and not `store.closeWindow`: the runtime is the surface
 * that emits `app-closed` and drops the launch args it holds, so a window closed
 * by a swap is bookkept exactly like one closed by the operator.
 */
export function closeOrphanWindows(runtime: ShellRuntime): string[] {
  const known = new Set(mergedApps(getRegistryState(runtime.registry)).map((app) => app.id));
  const closed: string[] = [];

  for (const window of Object.values(runtime.store.getState().windows)) {
    if (known.has(window.appId)) {
      continue;
    }
    runtime.closeWindow(window.id);
    closed.push(window.id);
  }

  return closed;
}

/**
 * Renames the custom-application windows whose title moved.
 *
 * A custom application's window title is a copy of its name taken when the window
 * opened. An operator who renames the application in the management window expects
 * the title bar to follow, and nothing in kit will do it: a window's title is host
 * data as far as the shell is concerned, and the dynamic area carries the new name
 * while the open window still carries the old one. The hand-written shell compared
 * the two on every Project change; this is that comparison.
 */
function syncCustomWindowTitles(runtime: ShellRuntime): void {
  const titles = new Map(
    mergedApps(getRegistryState(runtime.registry)).map((app) => [app.id, app.title]),
  );

  for (const window of Object.values(runtime.store.getState().windows)) {
    if (customApplicationProjectId(window.appId) === null) {
      continue;
    }
    const title = titles.get(window.appId);
    if (title && title !== window.title) {
      runtime.setTitle(window.id, title);
    }
  }
}

/**
 * Publishes one Project's custom applications, then cleans up after it.
 *
 * The order is the whole function. `closeOrphanWindows` reads the registry's
 * **merged** view, so it has to run after `setDynamicApps` — before it, the list
 * it checks is the previous Project's, and the windows that should survive the
 * switch are closed while the ones that should not are kept.
 *
 * Passing no `entryFor` is also the point: production serves the real content
 * layer, the iframe frame the manifest itself carries. The trial page overrides
 * that one slot to show a local panel; nothing here should.
 *
 * An empty list is a publish too. It is what a Project with no custom
 * applications, or one the operator cannot read, looks like — and publishing it
 * is what closes the windows left over from the Project that was just left,
 * which is the behaviour the hand-written shell had on a Project switch.
 */
export function publishCustomGroup(
  runtime: ShellRuntime,
  applications: readonly CustomApplication[],
): string[] {
  runtime.registry.setDynamicApps(buildCustomApplicationGroup(applications, bridgeSessionDeps));
  const closed = closeOrphanWindows(runtime);
  syncCustomWindowTitles(runtime);
  return closed;
}
