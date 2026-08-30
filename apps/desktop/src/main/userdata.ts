import * as path from 'node:path';
import * as fs from 'node:fs';

// e2eUserDataDir returns the Electron userData path for a given environment,
// or null when GRIDWELL_HOME is absent (a normal launch keeps the default
// ~/.config/gridwell-desktop).
//
// One owner: userData derives from GRIDWELL_HOME. The live app does not set
// it and keeps its default Chromium profile; every e2e run that sets it gets
// a private <home>/electron profile, never touching the live app's profile
// or lock file.
//
// Pure: no Electron import. Exercised through applyUserDataOverride.
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
  // Electron 28+ has sessionData as a separate path for the default session's
  // cookies, localStorage, cache, and IndexedDB. It inherits userData when
  // unset, but setting it explicitly keeps the two co-located regardless of
  // the order the paths are resolved in.
  try {
    setPath('sessionData', dir);
  } catch {
    // Pre-28 Electron does not know 'sessionData'; swallow.
  }
}
