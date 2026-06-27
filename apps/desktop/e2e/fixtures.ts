import { test as base, _electron as electron, ElectronApplication, Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';
import { GridwellDriver } from './driver';

// apps/desktop, and the repo root two levels up (where `make build` lays out the
// gridwell sidecar + plugin binaries and the web/ static dir).
const DESKTOP_DIR = path.resolve(__dirname, '..');
const REPO_ROOT = path.resolve(DESKTOP_DIR, '..', '..');

// seedHome creates a throwaway Gridwell home and registers one localdb plugin in
// it via `gridwell init` — the same coordinated setup a real user runs. server.yaml
// is mandatory (no fallback), so every launch points GRIDWELL_HOME at a home seeded
// this way. Returns the home dir; callers remove it on teardown.
export function seedHome(): string {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-e2e-'));
  execFileSync(path.join(REPO_ROOT, 'gridwell'), ['init', '--kind', 'localdb', '--name', 'e2e'], {
    env: { ...process.env, GRIDWELL_HOME: home },
    stdio: 'ignore',
  });
  return home;
}

type Fixtures = {
  electronApp: ElectronApplication;
  window: Page;
  gw: GridwellDriver;
};

// The e2e fixture launches the SAME `electron .` entry that `make launch` uses
// (apps/desktop/src/main/index.ts spawns the Go sidecar itself), so the whole
// stack — renderer → wasm → Connect-RPC → server → SQLite — is exercised. Each
// test gets a fresh temp home seeded with one localdb plugin; GRIDWELL_E2E=1
// turns on the renderer's read-only introspection hook.
export const test = base.extend<Fixtures>({
  electronApp: async ({}, use) => {
    const home = seedHome();
    const app = await electron.launch({
      args: ['.'],
      cwd: DESKTOP_DIR,
      env: {
        ...process.env,
        GRIDWELL_E2E: '1',
        GRIDWELL_HOME: home,
        GRIDWELL_SIDECAR: path.join(REPO_ROOT, 'gridwell'),
        GRIDWELL_STATIC: path.join(REPO_ROOT, 'web'),
      },
    });
    await use(app);
    await app.close().catch(() => {});
    fs.rmSync(home, { recursive: true, force: true });
  },

  window: async ({ electronApp }, use) => {
    const win = await electronApp.firstWindow();
    // The sidecar must report ready before the window opens, and the wasm must
    // boot and install the hook. Give the whole chain a generous budget.
    await win.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(win);
  },

  gw: async ({ window }, use) => {
    const origin = new URL(window.url()).origin;
    await use(new GridwellDriver(window, origin));
  },
});

export { expect } from '@playwright/test';
