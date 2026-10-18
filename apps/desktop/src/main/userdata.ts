import * as path from 'node:path';
import * as fs from 'node:fs';

// The Electron userData path derives from GRIDWELL_HOME and nothing else. A
// normal launch keeps the default profile; an e2e run gets a private
// ~/.gridwell/electron one and never touches the live app's profile or lock.
function e2eUserDataDir(env: Record<string, string | undefined>): string | null {
  const home = env['GRIDWELL_HOME'];
  if (!home) return null;
  return path.join(home, 'electron');
}

// Electron ignores a userData override after app.whenReady(), so this must be
// called before. setPath is app.setPath, passed in so this file needs no
// Electron import.
export function applyUserDataOverride(
  setPath: (name: string, value: string) => void,
  env: Record<string, string | undefined>,
): void {
  const dir = e2eUserDataDir(env);
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  setPath('userData', dir);
  // sessionData inherits userData when unset; setting it keeps the two
  // co-located whatever order the paths resolve in.
  try {
    setPath('sessionData', dir);
  } catch {
    // Electron before 28 does not know 'sessionData'.
  }
}
