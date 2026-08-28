import * as path from 'node:path';
import * as fs from 'node:fs';

// e2eUserDataDir returns the Electron userData path to use when GRIDWELL_HOME
// is set in the given environment. Returns null when GRIDWELL_HOME is absent
// (normal launch → keep the default ~/.config/gridwell-desktop).
//
// One owner: userData derives from GRIDWELL_HOME. The live app (which does not
// set GRIDWELL_HOME) keeps its default Chromium profile; every e2e run that
// sets GRIDWELL_HOME gets a private <home>/electron profile that never
// touches the live app's profile or lock file.
//
// Pure function (no Electron import); exercised through applyUserDataOverride.
function e2eUserDataDir(env: Record<string, string | undefined>): string | null {
  const home = env['GRIDWELL_HOME'];
  if (!home) return null;
  return path.join(home, 'electron');
}

// applyUserDataOverride redirects Electron's userData (and, for Electron ≥ 28,
// sessionData) to the per-run directory returned by e2eUserDataDir. Must be
// called before app.whenReady(); calling it after has no effect on Electron.
//
// setPath is app.setPath — passed as a parameter so the function stays
// unit-testable without an Electron import.
export function applyUserDataOverride(
  setPath: (name: string, value: string) => void,
  env: Record<string, string | undefined>,
): void {
  const dir = e2eUserDataDir(env);
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  setPath('userData', dir);
  // Electron 28+ introduced sessionData as a separately-settable path for the
  // default session's cookies, localStorage, cache, and IndexedDB. Without an
  // explicit override it inherits userData, but once userData is redirected we
  // set sessionData explicitly to keep them co-located and avoid any subtle
  // path-inheritance ordering surprises.
  try {
    setPath('sessionData', dir);
  } catch {
    // Pre-28 Electron does not know 'sessionData'; swallow.
  }
}
