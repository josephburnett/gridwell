import { test as base, _electron as electron, ElectronApplication, Page } from '@playwright/test';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';
import { GridwellDriver } from './driver';

// apps/desktop, and the repo root two levels up (where `make build` lays out the
// gridwell sidecar + plugin binaries and the web/ static dir).
const DESKTOP_DIR = path.resolve(__dirname, '..');
const REPO_ROOT = path.resolve(DESKTOP_DIR, '..', '..');

type Fixtures = {
  electronApp: ElectronApplication;
  window: Page;
  gw: GridwellDriver;
};

// The e2e fixture launches the SAME `electron .` entry that `make launch` uses
// (apps/desktop/src/main/index.ts spawns the Go sidecar itself), so the whole
// stack — renderer → wasm → Connect-RPC → server → SQLite — is exercised. Each
// test gets a fresh temp DB; GRIDWELL_E2E=1 turns on the renderer's read-only
// introspection hook.
export const test = base.extend<Fixtures>({
  electronApp: async ({}, use) => {
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-e2e-'));
    const app = await electron.launch({
      args: ['.'],
      cwd: DESKTOP_DIR,
      env: {
        ...process.env,
        GRIDWELL_E2E: '1',
        GRIDWELL_DB: path.join(tmp, 'e2e.db'),
        GRIDWELL_SIDECAR: path.join(REPO_ROOT, 'gridwell'),
        GRIDWELL_STATIC: path.join(REPO_ROOT, 'web'),
      },
    });
    await use(app);
    await app.close().catch(() => {});
    fs.rmSync(tmp, { recursive: true, force: true });
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
