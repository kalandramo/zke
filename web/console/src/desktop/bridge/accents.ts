import type { AppManifest as ZkeAppManifest } from "@/apps/types";
import type { AccentDefinition } from "@kalandramo/desktop-kit/registry";
import { appAccentVarNames } from "@kalandramo/desktop-kit-tokens";

/**
 * The accent ids the catalogue actually wears.
 *
 * Read off `APP_MANIFESTS` rather than written out again: `AppAccent` is a
 * compile-time union, so the only runtime enumeration available is the
 * applications themselves, and an accent no application declares is one the
 * shell is never asked to draw. Deriving also means a new application brings its
 * own accent with it instead of being registered as an unlit tile by someone who
 * forgot to add a name to a second list.
 */
export function zkeAccentIds(manifests: readonly ZkeAppManifest[]): string[] {
  const ids = new Set<string>();
  for (const manifest of manifests) {
    if (manifest.accent) {
      ids.add(manifest.accent);
    }
  }
  return [...ids];
}

/**
 * The three-piece definition for one accent, taken from the stylesheet.
 *
 * The hex values exist in exactly one place — `src/styles/app-accents.css`,
 * where six of the eleven are themselves aliases of the framework's built-in
 * hues — and copying them into TypeScript would create a second source that a
 * palette retune leaves behind. A copied palette fails the way this project has
 * already been failed once: silently, green through `tsc`, `eslint` and
 * `vite build`, with the tile rendering in no colour at all.
 *
 * So the values are read back out of the cascade at registration time.
 * `getComputedStyle` resolves a custom property's computed value with its own
 * `var()` references substituted, which is what makes the aliased six come home
 * as real colours instead of as the text `var(--app-blue-from)` — the aliasing
 * in that file stays invisible to the bridge.
 *
 * The one thing this costs is ordering: the stylesheet has to be applied before
 * the bridge runs. That holds for the current entry (`main.tsx` imports
 * `app-accents.css` at module scope, and registration happens on the way to
 * rendering `<Desktop>`), and a bridge that runs earlier gets the loud answer
 * below rather than a registry full of empty strings — `registerAccent` throws on
 * an empty `from`/`to`/`glow`, so a missing value is skipped here instead of
 * taking the whole registration down.
 */
export function readAccentDefinition(id: string): AccentDefinition | null {
  if (typeof document === "undefined") {
    console.warn(`[desktop/bridge] no document at registration; accent "${id}" was not declared.`);
    return null;
  }

  const vars = appAccentVarNames(id);
  const style = getComputedStyle(document.documentElement);
  const from = style.getPropertyValue(vars.from).trim();
  const to = style.getPropertyValue(vars.to).trim();
  const glow = style.getPropertyValue(vars.glow).trim();

  const pairs: Array<[value: string, name: string]> = [
    [from, vars.from],
    [to, vars.to],
    [glow, vars.glow],
  ];
  const missing = pairs.filter(([value]) => value === "").map(([, name]) => name);

  if (missing.length > 0) {
    console.warn(
      `[desktop/bridge] accent "${id}" is missing ${missing.join(", ")} in the cascade; ` +
        `the tile would render unlit, so the definition was not registered.`,
    );
    return null;
  }

  return { id, from, to, glow };
}
