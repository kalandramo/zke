import { useState } from "react";
import type { ReactNode, SVGProps } from "react";
import { ExternalLink } from "lucide-react";

import { appFaceClass } from "@/apps/accent";
import { createCustomApplicationManifest } from "@/apps/registry";
import type { AppManifest as ZkeAppManifest } from "@/apps/types";
import type { CustomApplication } from "@/api/types";
import { cn } from "@/lib/cn";
import type {
  AppIconComponent,
  AppManifest as KitAppManifest,
  DockItem as KitDockItem,
} from "@kalandramo/desktop-kit/registry";

import { createAppEntry } from "./app-adapter";
import { toCapabilityGates, type ZkeBridgeDeps } from "./capabilities";

/**
 * The icon props the kit shell hands out — `className` for size and ink,
 * `style` for the planned grey, `strokeWidth` and `aria-hidden` for the glyph.
 * It is spelled out here rather than imported because the bridge is the place
 * that has to stay honest about which of them a server-supplied logo can use.
 */
type ShellIconProps = Pick<
  SVGProps<SVGSVGElement>,
  "className" | "style" | "strokeWidth" | "aria-hidden"
>;

/**
 * The icon for one custom application, built **once per manifest**.
 *
 * The factory exists because the logo has a fallback and the fallback is
 * component state: `onError` flips to the catalogue icon and stays there. If the
 * component type were created inside a render, every re-render would hand the
 * shell a different type, React would unmount and remount the tile, the failure
 * would be forgotten, and a dead logo URL would retry forever — a broken image
 * that never settles into its fallback, on every frame the desktop draws.
 *
 * It does not delegate to `apps/AppGlyph`, even though that is the same rule in
 * the ZKE launcher: `AppGlyph` takes `{ manifest, className }` and hard-codes
 * the rest, so routing through it would drop the shell's `style` and
 * `strokeWidth` on the floor. The fallback branch here forwards all four, which
 * is what the widened `icon` field was added to make possible.
 */
export function makeCustomAppIcon(zke: ZkeAppManifest): AppIconComponent {
  const logoURL = zke.customApplication?.logo_url;
  const Fallback = zke.icon;

  return function CustomAppIcon({
    className,
    style,
    strokeWidth,
    "aria-hidden": ariaHidden,
  }: ShellIconProps) {
    const [logoFailed, setLogoFailed] = useState(false);

    if (logoURL && !logoFailed) {
      return (
        <img
          src={logoURL}
          alt=""
          className={cn(className, "object-contain")}
          style={style}
          referrerPolicy="no-referrer"
          onError={() => setLogoFailed(true)}
        />
      );
    }
    return (
      <Fallback
        className={className}
        style={style}
        strokeWidth={strokeWidth ?? 1.75}
        aria-hidden={ariaHidden ?? true}
      />
    );
  };
}

/**
 * The window's own escape hatch: a custom application is somebody else's page,
 * and the honest action on the title bar is "take me there".
 *
 * It is a bare `<a>` because the slot has no wrapper — the node becomes a direct
 * child of the header's flex row, so everything that makes it a legible control
 * (height, padding, the gap to the window buttons, the focus ring) is declared
 * here and nowhere else. `shrink-0` matters: the title is the truncating child,
 * but a header that gets crowded will otherwise clip this instead, and a clipped
 * escape hatch is worse than no escape hatch.
 *
 * `onPointerDown` stops propagation because the header is the drag handle. A
 * link that also starts a window drag is a link that does not open on the first
 * try; kit's own window buttons do exactly the same thing for the same reason.
 */
export function makeCustomAppTitleBar(zke: ZkeAppManifest): ReactNode {
  const application = zke.customApplication;
  if (!application) {
    return null;
  }

  return (
    <a
      data-testid="custom-app-open-in-new-tab"
      href={application.url}
      target="_blank"
      rel="noreferrer"
      title={`在新标签页打开 ${application.name}`}
      onPointerDown={(event) => event.stopPropagation()}
      className="text-muted-foreground hover:bg-surface-raised hover:text-foreground dk-focus rounded-control inline-flex h-6 shrink-0 items-center gap-1 px-1.5 text-xs whitespace-nowrap"
    >
      <ExternalLink className="size-3.5" aria-hidden />
      在新标签页打开
    </a>
  );
}

/**
 * The coloured-face override for one custom application, and why only half of `appFaceClass`
 * is ever passed.
 *
 * A Server-supplied logo is drawn on a light surface (`--app-logo-surface`) because a pale mark
 * disappears on an accent gradient. That face is the application's identity, and it is already
 * decided in `apps/accent.ts` — the management page draws it there. The launcher and the Dock
 * have to agree with it, which is what `faceClass` is for: kit replaces its own gradient face
 * with the host's class string, and it does so for the tile *and* the Dock item, so the two
 * places stop being two backgrounds for one icon.
 *
 * Only the logo case is passed. Without a logo the glyph is a lucide `Globe2`, and the right face
 * for that is kit's own default: `dk-tile` plus an inline gradient. `appFaceClass`'s fallback
 * branch is a *class* implementation of a gradient (`bg-linear-to-b from-[var(--app-*-from)]`),
 * a second way of drawing the same thing that does not quite agree with the first — passing it
 * would put two renderers on one tile. So no logo means no field, and kit's face stands —
 * ink included: on a gradient face kit writes `text-white` itself, and that is correct there.
 *
 * `planned` needs no branch here: custom applications are always `available` (the registry builds
 * them that way), and kit would ignore `faceClass` for a planned app anyway.
 *
 * The ink lives in `appFaceClass` itself now, not here: the management page draws the same
 * face with none of kit's `faceClass` machinery in the picture, so naming it at the source
 * is the only way the launcher, the Dock and that page stop being three chances to forget
 * it. What is left here is the gate — pass the face only when there is a logo to carry it.
 */
function customAppFaceClass(zke: ZkeAppManifest): string | undefined {
  if (!zke.customApplication?.logo_url) {
    return undefined;
  }
  return appFaceClass(zke);
}

/**
 * One custom application, on the kit registration surface.
 *
 * Same shape as `toKitAppManifest`, with the two fields that catalogue
 * applications do not need: an `icon` that is a remote logo rather than a lucide
 * glyph, and a `titleBarExtra`. The `icon` is a parameter rather than built
 * inside because the Dock tile has to be handed the *same* component — two
 * instances would mean two independent fallback states, and the Dock and the
 * launcher would then disagree about whether the logo is broken.
 */
export function toCustomKitApp(
  zke: ZkeAppManifest,
  deps: ZkeBridgeDeps,
  icon: AppIconComponent = makeCustomAppIcon(zke),
): KitAppManifest {
  return {
    id: zke.id,
    title: zke.title,
    description: zke.description,
    icon,
    accent: zke.accent,
    capabilities: toCapabilityGates(zke, deps),
    availability: zke.availability,
    defaultSize: zke.defaultSize,
    keepInDock: false,
    titleBarExtra: makeCustomAppTitleBar(zke),
    faceClass: customAppFaceClass(zke),
    mount: "react",
    entry: createAppEntry(zke),
  };
}

/** The Dock tile for one custom application; `dock-` prefix as in `toDockItem`. */
export function toCustomDockItem(zke: ZkeAppManifest, icon: AppIconComponent): KitDockItem {
  return {
    id: `dock-${zke.id}`,
    appId: zke.id,
    title: zke.title,
    icon,
    accent: zke.accent,
    // Given a second time on purpose: the Dock reads only its own item's field, it does not
    // fall back to the manifest the way `accent` does. One application, two places, same face.
    faceClass: customAppFaceClass(zke),
  };
}

/** One whole dynamic area: the applications and their Dock tiles, in catalogue order. */
export type CustomApplicationGroup = {
  apps: KitAppManifest[];
  dockItems: KitDockItem[];
};

export type CustomApplicationGroupOptions = {
  /**
   * Replaces the content layer for every application in the group.
   *
   * The production content is `CustomApplicationFrame`, an iframe pointed at the
   * application's own origin. A host that has to observe the mount — a trial
   * page proving that `restart` remounts the content — supplies its own
   * component here and still goes through `createAppEntry`, so the adapter the
   * eleven catalogue applications use is the thing under test, not something the
   * bypass quietly replaces.
   */
  entryFor?: (application: CustomApplication) => ZkeAppManifest["entry"];
};

/**
 * The whole dynamic area for one Project's custom applications, ready for
 * `registry.setDynamicApps`.
 *
 * This is the host's side of the 0.8.0 dynamic channel: the Server answers with
 * a list, the bridge turns it into manifests, and one call replaces the previous
 * list. It is a pure function of its input — no registry, no store, no window
 * touched — so the caller decides when the swap happens and what to clean up
 * afterwards (`closeOrphanWindows` in `./dynamic`).
 *
 * The manifest is rebuilt from the application rather than cached, and that is
 * deliberate: `setDynamicApps` replaces the whole group, so a cached icon
 * component would be a stale reference held across a swap nobody asked for.
 * Fresh per call, stable for as long as the group is installed.
 */
export function buildCustomApplicationGroup(
  applications: readonly CustomApplication[],
  deps: ZkeBridgeDeps,
  options: CustomApplicationGroupOptions = {},
): CustomApplicationGroup {
  const apps: KitAppManifest[] = [];
  const dockItems: KitDockItem[] = [];

  for (const application of applications) {
    const base = createCustomApplicationManifest(application);
    const zke: ZkeAppManifest = options.entryFor
      ? { ...base, entry: options.entryFor(application) }
      : base;
    const icon = makeCustomAppIcon(zke);
    apps.push(toCustomKitApp(zke, deps, icon));
    dockItems.push(toCustomDockItem(zke, icon));
  }

  return { apps, dockItems };
}
