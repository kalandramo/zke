import { APP_MANIFESTS } from "@/apps/registry";
import type { DesktopRegistry } from "@kalandramo/desktop-kit/registry";

import { readAccentDefinition, zkeAccentIds } from "./accents";
import { toDockItem, toKitAppManifest } from "./manifest";
import type { ZkeBridgeDeps } from "./capabilities";

export type { ZkeBridgeDeps } from "./capabilities";
export type { CustomApplicationGroup, CustomApplicationGroupOptions } from "./custom-apps";
export {
  buildCustomApplicationGroup,
  makeCustomAppIcon,
  makeCustomAppTitleBar,
  toCustomDockItem,
  toCustomKitApp,
} from "./custom-apps";
export { closeOrphanWindows, publishCustomGroup } from "./dynamic";
export { readAccentDefinition, zkeAccentIds } from "./accents";
export { toDockItem, toKitAppManifest } from "./manifest";

/** What one pass of the bridge put on the registration surface. */
export type RegistrationCounts = {
  apps: number;
  dockItems: number;
  accents: number;
};

/**
 * Per-registry results, kept in a `WeakMap` rather than a module boolean.
 *
 * A boolean guard is what kit's own skeleton uses, and it is right for one
 * registry; it is wrong the moment a second registration surface exists — a
 * `createRegistry()` for a test, or a future isolated desktop — because the guard
 * would report success for a registry that was never touched. Keying on the
 * instance makes the call idempotent where it is idempotent and honest where it
 * is not, and the WeakMap lets an isolated registry be collected again.
 */
const registrations = new WeakMap<DesktopRegistry, RegistrationCounts>();

/**
 * Puts the ZKE catalogue on a kit registration surface: eleven applications, the
 * Dock tiles for the available ones, and the accent definitions the tiles are
 * drawn with.
 *
 * Must run before `<Desktop>` mounts. Kit freezes the registry on first mount
 * and every later `register*` is a warning and a dropped call, so a bridge
 * invoked from an effect inside the shell would register nothing at all and say
 * so only in the console. The caller's job is to run it on the way to rendering,
 * the way `src/kit-lab/main.tsx` does.
 *
 * Idempotent per registry instance: a second call returns the first call's
 * counts without re-registering, which is what StrictMode's double render and
 * Vite's HMR re-evaluation need — neither is a new set of applications.
 *
 * The counts are what to check after wiring this up. They are returned rather
 * than logged because the Console has no test layer to assert them against, so
 * the numbers have to reach the caller to mean anything.
 */
export function registerZkeApps(
  registry: DesktopRegistry,
  deps: ZkeBridgeDeps,
): RegistrationCounts {
  const settled = registrations.get(registry);
  if (settled) {
    return settled;
  }

  if (registry.isRegistryFrozen()) {
    console.warn(
      "[desktop/bridge] the registry is already frozen (<Desktop> has mounted); nothing was registered.",
    );
    return { apps: 0, dockItems: 0, accents: 0 };
  }

  let accents = 0;
  for (const id of zkeAccentIds(APP_MANIFESTS)) {
    const definition = readAccentDefinition(id);
    if (definition) {
      registry.registerAccent(definition);
      accents += 1;
    }
  }

  let apps = 0;
  let dockItems = 0;
  for (const zke of APP_MANIFESTS) {
    registry.registerApp(toKitAppManifest(zke, deps));
    apps += 1;
    // A planned application gets no tile: the launcher is where the product
    // shape is legible, and a Dock of running applications has nothing running
    // in it to point at.
    if (zke.availability.state === "available") {
      registry.registerDockItem(toDockItem(zke));
      dockItems += 1;
    }
  }

  const counts = { apps, dockItems, accents };
  registrations.set(registry, counts);
  return counts;
}
