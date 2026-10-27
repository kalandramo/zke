import { lazy, Suspense, useCallback, useEffect, useMemo, useRef } from "react";
import type { ComponentType, LazyExoticComponent } from "react";

import type { AppComponentProps, AppManifest as ZkeAppManifest } from "@/apps/types";
import { LoadingState } from "@/components/common/state";
import type { AppInstanceProps, ShellApi } from "@kalandramo/desktop-kit/registry";

/**
 * The `args` key a bridged launch uses to carry the window title the caller
 * asked for.
 *
 * `ShellApi.openApp` takes `args` and nothing else, while ZKE's own `openApp`
 * takes `{ title }` — 组织与资源 opens 集群接入管理 as「集群接入管理 · <项目>」and
 * 应用中心 opens a custom application under its own name. The title therefore
 * travels as data rather than as a second parameter, and the adapter below is
 * what turns it back into `shell.setTitle` on the window that arrives.
 */
const TITLE_ARG = "title";

function titleFromArgs(args: Record<string, unknown> | undefined): string | null {
  const title = args?.[TITLE_ARG];
  return typeof title === "string" && title.length > 0 ? title : null;
}

/**
 * The `entry` a bridged application registers under.
 *
 * Kit hands an application `{ launch, manifest, shell }`; every ZKE application
 * was written against `{ windowId, manifest, openApp }`, where `manifest` is
 * ZKE's own — the one carrying `customApplication`, `requiredPermissions` and
 * the planned capabilities a window reads to describe itself. Neither side can
 * be pointed at the other, so each application gets this one thin frame that
 * speaks kit on the outside and ZKE on the inside, and no application file has
 * to change for the shell to swap.
 *
 * The wrapper is itself `lazy` over an already-resolved promise, which is the
 * shape kit's own skeleton uses. It costs one microtask and buys the important
 * part: nothing here imports an application module, so the eleven chunks stay
 * behind the eleven `lazy()` boundaries `apps/registry.ts` already declares.
 * The adapter renders `zke.entry` inside a `Suspense` of its own so that
 * boundary is what waits for the application, and the shell's fallback is not
 * asked to cover a component it does not know about.
 */
export function createAppEntry(
  zke: ZkeAppManifest,
): LazyExoticComponent<ComponentType<AppInstanceProps>> {
  return lazy(() => Promise.resolve({ default: makeAdapter(zke) }));
}

function makeAdapter(zke: ZkeAppManifest): ComponentType<AppInstanceProps> {
  const Entry = zke.entry;

  function ZkeAppAdapter({ launch, shell }: AppInstanceProps) {
    /*
     * The latest `shell` behind a reference, so `openApp` can stay one function
     * for the life of the window.
     *
     * ZKE applications hold `openApp` in `useMemo`'d props and in effect
     * dependencies; a new identity every render would re-run their data effects,
     * which is exactly what `desktop/Window.tsx` takes care not to do while a
     * window is merely being dragged. Kit builds its shell per window, so this
     * normally never changes — but the bridge is the place that has to be right
     * about what the shell does next release, not merely right today.
     */
    const shellRef = useRef<ShellApi>(shell);
    useEffect(() => {
      shellRef.current = shell;
    }, [shell]);

    const openApp = useCallback<AppComponentProps["openApp"]>((appId, options) => {
      shellRef.current.openApp(appId, options?.title ? { [TITLE_ARG]: options.title } : undefined);
    }, []);

    const appProps = useMemo<AppComponentProps>(
      () => ({ windowId: launch.windowId, manifest: zke, openApp }),
      [launch.windowId, openApp],
    );

    /*
     * A title that arrived in `args` is applied here rather than at open time,
     * because the window this bridge opens is created by kit and kit names it
     * from the manifest alone.
     *
     * Keyed on the title rather than on the launch object: re-opening an
     * application that is already open is a new launch with a new title, and the
     * window must take it — that is what 组织与资源 does when the operator
     * switches Project. A launch that carries no title leaves the window's own
     * name alone rather than resetting it to the manifest's.
     */
    const requestedTitle = titleFromArgs(launch.args);
    useEffect(() => {
      if (requestedTitle !== null) {
        shellRef.current.setTitle(requestedTitle);
      }
    }, [requestedTitle]);

    return (
      <Suspense fallback={<LoadingState label="正在加载应用…" />}>
        <Entry {...appProps} />
      </Suspense>
    );
  }

  return ZkeAppAdapter;
}
