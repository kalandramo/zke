import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  Desktop as KitDesktop,
  ShellRuntimeProvider,
  createChromeAttentionHub,
  createShellRuntime,
} from "@kalandramo/desktop-kit";
import type { ShellLayout, ShellRuntime } from "@kalandramo/desktop-kit";
import { createRegistry } from "@kalandramo/desktop-kit/registry";
import { useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";

import { useClusterEvents } from "@/api/events";
import { useCustomApplications } from "@/api/queries/custom-applications";
import { projectListQueryOptions, tenantListQueryOptions } from "@/api/queries/resources";
import { stashEvidenceTarget } from "@/apps/evidence-link";
import { isCustomApplicationManifestId } from "@/apps/registry";
import { useSessionContext } from "@/auth/session-context";
import { ZkeMark } from "@/components/brand/zke-mark";
import { SCOPE_ATTENTION_MS, useScopeStore } from "@/scope/scope-store";
import { THEME_STORAGE_KEY, useThemeStore } from "@/theme/theme-store";

import { registerZkeApps } from "./bridge";
import { publishCustomGroup } from "./bridge/dynamic";
import { bridgeSessionDeps, setBridgeSession } from "./bridge/session";
import {
  clearDesktopState,
  createZkePersistenceAdapter,
  readDesktopLayout,
  readStoredWindowAppIds,
} from "./persistence";
import {
  installShell,
  readHostExtensions,
  setDockVisiblePreference,
  uninstallShell,
} from "./shell";
import { DesktopStatus, ScopeLead, ThemeControl } from "./TopBar";

/**
 * The desktop's own measurements, in the units kit lays out in.
 *
 * Every number here is the number the hand-written shell used, so a window
 * restored from an old layout lands where it was. `chromeMode: "overlay"` is the
 * one that is not merely a measurement: it is what lets a maximized window run
 * under the bar rather than below it, which is how the bar reads as glass over the
 * desktop instead of a strip the wallpaper has to stop at.
 */
const SHELL_LAYOUT: Partial<ShellLayout> = {
  topBarHeight: 44,
  dockHeight: 84,
  margin: 12,
  minWindowSize: { width: 380, height: 260 },
  chromeMode: "overlay",
};

/** The debounce the hand-written shell waited with before it wrote a layout. */
const PERSIST_DEBOUNCE_MS = 400;

/**
 * How long restoring waits for the Project's custom-application list, at most.
 *
 * Long enough for a normal request, short enough that a Server which is not
 * answering cannot hold the whole desktop behind a loading screen. Past the cap
 * the desktop opens and an operator loses the custom windows they had open — the
 * same thing that happens when the list arrives and says those applications are
 * gone, so the failure is familiar rather than new.
 */
const DYNAMIC_APPS_WAIT_MS = 1_200;

/**
 * Builds the shell for one signed-in operator.
 *
 * A fresh registry per operator is not a detail. The dynamic area holds the custom
 * applications of whichever Project was last open, and a runtime that outlived a
 * sign-out would hand those applications — and their windows — to the next person
 * to log in on the same browser.
 */
function createZkeShell(userId: string): ShellRuntime {
  const registry = createRegistry();
  registerZkeApps(registry, bridgeSessionDeps);

  return createShellRuntime({
    registry,
    adapter: createZkePersistenceAdapter(userId),
    autosaveDelayMs: PERSIST_DEBOUNCE_MS,
    cascade: true,
    snapMode: "edge",
    getTheme: () => {
      const theme = useThemeStore.getState().theme;
      return { name: theme, preference: theme };
    },
    setTheme: (theme) => {
      // The snapshot carries a *copy* of the theme. `zke.theme` and the dataset are
      // the truth — index.html resolved them before React ran — so a copy taken at
      // save time must not overwrite a choice made since, which is exactly what
      // would happen when a second tab changes the theme. The snapshot only ever
      // fills in a desktop that has no stored opinion.
      let stored: string | null;
      try {
        stored = localStorage.getItem(THEME_STORAGE_KEY);
      } catch {
        stored = null;
      }
      if (stored === "light" || stored === "dark") {
        return;
      }
      useThemeStore.getState().setTheme(theme.name === "dark" ? "dark" : "light");
    },
    getExtensions: readHostExtensions,
  });
}

/**
 * The ZKE Web desktop: kit's shell, holding ZKE's applications.
 *
 * Everything the shell used to do by hand — window geometry, dragging, snapping,
 * the Dock, the launcher, the top bar's own layout — is kit's now. What is left
 * here is what only this product can decide: which applications exist and who may
 * see them, which Project the desktop is working in, whose layout to restore, and
 * the deep link that opens a window before the operator asks for one.
 */
export function Desktop() {
  const queryClient = useQueryClient();
  const { session, permissions } = useSessionContext();
  const userId = session?.user.id ?? null;

  const shell = useMemo(() => (userId ? createZkeShell(userId) : null), [userId]);
  const attentionHub = useMemo(() => createChromeAttentionHub(), []);

  const globalScope = useScopeStore((state) => state.scope);
  const setScope = useScopeStore((state) => state.setScope);
  const hydrateScope = useScopeStore((state) => state.hydrate);
  const resetScope = useScopeStore((state) => state.reset);

  /**
   * The layout as it sits on disk, read once per operator.
   *
   * Read during render rather than inside the restore effect, because two things
   * need it before anything is awaited: the Dock's first paint (a saved-hidden Dock
   * must not flicker into view) and the decision of whether restoring has to wait
   * for the custom-application list.
   */
  const storedLayout = useMemo(() => (userId ? readDesktopLayout(userId) : null), [userId]);
  const storedScope = storedLayout?.host.scope ?? null;

  // A layout that saved a custom application's window cannot be restored until that
  // application is registered again: kit drops windows whose appId the registry
  // does not know, and the custom ones only arrive with the Project's list. Restoring
  // without waiting would silently lose them; the wait is capped below so that
  // losing them slowly is never required.
  const storedNeedsDynamicApps = useMemo(
    () => (userId ? readStoredWindowAppIds(userId).some(isCustomApplicationManifestId) : false),
    [userId],
  );
  const canReadStoredProject = permissions.can("project.read", {
    type: "project",
    tenantId: storedScope?.tenantId ?? null,
    projectId: storedScope?.projectId ?? null,
  });

  const canReadSelectedProject = permissions.can("project.read", {
    type: "project",
    tenantId: globalScope.tenantId,
    projectId: globalScope.projectId,
  });
  const customApplicationsQuery = useCustomApplications(
    globalScope.projectId,
    canReadSelectedProject,
  );
  const applications = customApplicationsQuery.data?.applications;

  const [dockVisible, setDockVisible] = useState(() => storedLayout?.host.dockVisible ?? true);
  const [restored, setRestored] = useState(false);
  const hydratedFor = useRef<string | null>(null);

  // The Server refuses the event stream to a caller who can observe no Cluster at
  // all, and `EventSource` never exposes that status, so an ungated stream would
  // reconnect forever. Mirror the Server's own check here: it resolves
  // `cluster.read` visibility across every binding, which is what `canAnywhere`
  // reports.
  const clusterEventsAllowed = Boolean(session) && permissions.canAnywhere("cluster.read");
  const { state: streamState, lastEventAt } = useClusterEvents(clusterEventsAllowed);

  const [waitedTooLong, setWaitedTooLong] = useState(false);
  useEffect(() => {
    if (!storedNeedsDynamicApps || customApplicationsQuery.isFetched) {
      return;
    }
    const timer = window.setTimeout(() => setWaitedTooLong(true), DYNAMIC_APPS_WAIT_MS);
    return () => {
      window.clearTimeout(timer);
    };
  }, [storedNeedsDynamicApps, customApplicationsQuery.isFetched]);

  const readyForRestore =
    !storedNeedsDynamicApps ||
    !storedScope?.projectId ||
    !canReadStoredProject ||
    customApplicationsQuery.isFetched ||
    waitedTooLong;

  /**
   * Hands the shell the session it authorizes against, and takes it back on the way
   * out.
   *
   * The accessors close over the current render's values instead of a snapshot taken
   * once, because a registration made at mount must not freeze the answer for the
   * whole session: a role change has to reach the gates on the next render rather
   * than the next reload.
   *
   * Clearing the bridge is the logout path: with no session installed every
   * capability predicate denies, so a desktop caught mid-teardown cannot open
   * something it should not have.
   */
  useEffect(() => {
    if (!shell) {
      return;
    }
    setBridgeSession({ permissions: () => permissions, session: () => session });
    installShell(shell);
    return () => {
      uninstallShell(shell);
      setBridgeSession(null);
    };
  }, [shell, session, permissions]);

  /**
   * The Project the desktop works in, restored before anything is drawn.
   *
   * The scope store outlives the authenticated desktop. Without resetting it, a
   * second operator in the same browser session could inherit the first one's
   * Project before their own saved state is considered.
   */
  useEffect(() => {
    if (!userId) {
      return;
    }
    if (storedScope) {
      hydrateScope(storedScope);
    } else {
      resetScope();
    }
  }, [userId, storedScope, hydrateScope, resetScope]);

  // The Dock is shown or hidden by the operator, never by pointer proximity, and
  // the choice holds whether or not a window is full screen. The value lives in
  // React state because the bar reads it, and in the shell's extension slot because
  // persistence has to be able to read it from outside React.
  useEffect(() => {
    setDockVisiblePreference(dockVisible);
  }, [dockVisible]);

  const toggleDock = useCallback(() => setDockVisible((shown) => !shown), []);

  /*
   * A view inside a window can ask for the Project picker to be pointed at, and a
   * full-screen window is exactly the case where that picker is off screen — which
   * is also the case where an operator is most likely to have never seen it. The
   * bar comes back for as long as the highlight runs and then leaves again; the
   * window is not disturbed.
   *
   * The host does not tell the bar about it. It reports into the same attention hub
   * the bar already watches, so the request and the reply travel through one
   * mechanism instead of a prop only one surface understands.
   */
  useEffect(() => {
    let release: (() => void) | null = null;
    const unsubscribe = useScopeStore.subscribe((state, previous) => {
      if (state.attentionToken === previous.attentionToken) {
        return;
      }
      // A second request restarts the hold rather than adding to it: the operator
      // asked for the picker now, and the clock runs from that.
      release?.();
      release = attentionHub.report({ ttlMs: SCOPE_ATTENTION_MS });
    });
    return () => {
      unsubscribe();
      release?.();
    };
  }, [attentionHub]);

  // The Project's custom applications are the shell's dynamic area: registered when
  // the list arrives, and taken away when the Project changes, which is what closes
  // the windows that came with it.
  //
  // Declared before the restore below on purpose. Kit drops a window whose appId the
  // registry does not know, and a custom application is only in that registry after
  // this publish — so the publish has to have run when the snapshot is read, not
  // merely before the next paint.
  useEffect(() => {
    if (!shell) {
      return;
    }
    publishCustomGroup(shell, applications ?? []);
  }, [shell, applications]);

  // Restore the previous layout for this operator, then honour a deep link.
  useEffect(() => {
    if (!shell || !userId || !readyForRestore || hydratedFor.current === userId) {
      return;
    }
    hydratedFor.current = userId;

    let cancelled = false;
    void (async () => {
      await shell.restore();
      if (cancelled) {
        return;
      }

      const params = new URLSearchParams(window.location.search);
      const appId = params.get("app");
      const tenantId = params.get("tenant");
      const projectId = params.get("project");
      const clusterId = params.get("cluster");
      const namespace = params.get("namespace");
      const evidenceKind = params.get("evidence_kind");
      const gvk = params.get("gvk");
      const resource = params.get("resource");
      const query = params.get("query");

      // A deep link may carry the Project to work in. It arrives as bare ids; the
      // picker resolves the labels, and the Server still authorizes every request
      // against it, so an unreachable Project simply yields empty views.
      if (tenantId && projectId) {
        setScope({ tenantId, tenantName: null, projectId, projectName: null });
        if (clusterId && appId) {
          stashEvidenceTarget({
            appId,
            projectId,
            clusterId,
            namespace,
            evidenceKind,
            gvk,
            resource,
            query,
          });
        }
      }

      const restoredProjectId = tenantId && projectId ? projectId : storedScope?.projectId;
      if (!restoredProjectId) {
        void (async () => {
          try {
            const tenants = await queryClient.fetchQuery(
              tenantListQueryOptions({ limit: 1, offset: 0, status: "active" }),
            );
            const tenant = tenants.tenants[0];
            if (!tenant || cancelled) {
              return;
            }
            const projects = await queryClient.fetchQuery(
              projectListQueryOptions(tenant.id, {
                limit: 1,
                offset: 0,
                status: "active",
              }),
            );
            const project = projects.projects[0];
            // The operator may choose a Project while these requests are in
            // flight. Their explicit choice must win over the login default.
            if (cancelled || !project || useScopeStore.getState().scope.projectId) {
              return;
            }
            setScope({
              tenantId: tenant.id,
              tenantName: tenant.name,
              projectId: project.id,
              projectName: project.name,
            });
          } catch {
            // The desktop and its picker remain usable when default resolution
            // fails; nothing is claimed about a Project that was not reached.
          }
        })();
      }

      // Kit refuses rather than throwing when an application is unknown or planned,
      // so a bad `?app=` value is simply no window.
      if (appId) {
        shell.openApp(appId);
      }

      const url = new URL(window.location.href);
      for (const key of [
        "app",
        "tenant",
        "project",
        "cluster",
        "namespace",
        "evidence_kind",
        "gvk",
        "resource",
        "query",
      ]) {
        url.searchParams.delete(key);
      }
      window.history.replaceState({}, "", url);

      setRestored(true);
    })();

    return () => {
      cancelled = true;
      if (hydratedFor.current === userId) {
        hydratedFor.current = null;
      }
    };
  }, [shell, userId, readyForRestore, storedScope, setScope, queryClient]);

  // Autosave covers the windows themselves. The two host-owned fields are not store
  // state, so nothing in kit would ever write them: a Project selection or a hidden
  // Dock has to be persisted by the code that owns it.
  useEffect(() => {
    if (!shell || !restored) {
      return;
    }
    void shell.persist();
  }, [shell, restored, globalScope, dockVisible]);

  useEffect(() => {
    if (!shell || !restored) {
      return;
    }
    return shell.startAutosave();
  }, [shell, restored]);

  /*
   * The window-cycle chord stays the host's, and its direction stays ZKE's: plain
   * steps forward, Shift steps back. Kit's own chord is the reverse, and a shell
   * default is not worth an operator relearning a reflex, so the host binds it and
   * kit's stays off.
   *
   * The chord is matched on `event.code` — the physical key — not on `event.key`,
   * the character that key types. Shift changes the character: Ctrl+Shift+` arrives
   * as `key: "~"` (measured in Chromium, and pinned by `desktop-prod.spec.ts`), so
   * testing the glyph made the Shift branch unreachable and the reverse cycle a
   * shortcut that does nothing at all. A key position is also what an operator
   * reaches for, which is why the layout-independent `code` is the right subject to
   * bind: it fixes the Shift case, and every layout where that key types no grave
   * accent, and costs nothing the glyph test was buying.
   */
  useEffect(() => {
    if (!shell) {
      return;
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.code === "Backquote") {
        event.preventDefault();
        shell.cycleFocus(event.shiftKey ? -1 : 1);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [shell]);

  const handleOpenApp = useCallback(
    (appId: string) => {
      shell?.openApp(appId);
    },
    [shell],
  );

  const handleResetDesktop = useCallback(() => {
    if (!shell) {
      return;
    }
    shell.closeAll();
    // The scope store is not part of the layout, and an operator who resets the
    // desktop is asking to start over — including where they are looking.
    resetScope();
    if (userId) {
      clearDesktopState(userId);
    }
  }, [shell, resetScope, userId]);

  if (!userId || !shell || !restored) {
    return (
      <div className="from-desktop-from to-desktop-to flex h-full items-center justify-center bg-linear-to-br">
        <span className="text-muted-foreground flex items-center gap-2 text-[13px]">
          <Loader2 className="size-4 animate-spin" />
          正在恢复桌面…
        </span>
      </div>
    );
  }

  return (
    <ShellRuntimeProvider runtime={shell}>
      <KitDesktop
        brand={<ZkeMark className="size-6 rounded-[8px]" />}
        lead={<ScopeLead />}
        status={
          <DesktopStatus
            streamState={streamState}
            lastEventAt={lastEventAt}
            onOpenApp={handleOpenApp}
            onResetDesktop={handleResetDesktop}
          />
        }
        themeControl={<ThemeControl />}
        layout={SHELL_LAYOUT}
        immersive
        chromeAttentionHub={attentionHub}
        revealPresentation="sliver"
        revealOnDesktopClick
        narrowViewportLayout="stacked"
        dockRunningAppClick="taskbar"
        dockVisible={dockVisible}
        onDockToggle={toggleDock}
      />
    </ShellRuntimeProvider>
  );
}
