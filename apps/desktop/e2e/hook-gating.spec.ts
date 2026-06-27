import { test as base, expect, _electron as electron } from '@playwright/test';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { test, seedHome } from './fixtures';

const DESKTOP_DIR = path.resolve(__dirname, '..');
const REPO_ROOT = path.resolve(DESKTOP_DIR, '..', '..');

// The introspection hook is compiled into the normal wasm binary, so its safety
// rests entirely on the ?e2e=1 gate. These two tests pin both sides of the gate.

// Under the e2e fixture (GRIDWELL_E2E=1 → ?e2e=1) the hook is present.
test('hook is installed under ?e2e=1', async ({ window }) => {
  expect(await window.evaluate(() => typeof (window as any).__gridwellTest)).toBe('object');
  expect(await window.evaluate(() => new URL(location.href).searchParams.get('e2e'))).toBe('1');
});

// A normal launch (no GRIDWELL_E2E) must NOT expose the hook — this is what
// keeps the surface out of production.
base('hook is absent without the flag', async () => {
  const home = seedHome();
  const app = await electron.launch({
    args: ['.'],
    cwd: DESKTOP_DIR,
    env: {
      ...process.env,
      GRIDWELL_E2E: '', // explicitly off
      GRIDWELL_HOME: home,
      GRIDWELL_SIDECAR: path.join(REPO_ROOT, 'gridwell'),
      GRIDWELL_STATIC: path.join(REPO_ROOT, 'web'),
    },
  });
  try {
    const win = await app.firstWindow();
    // Wait for the canvas app to boot (the canvas element exists once wasm runs).
    await win.waitForFunction(() => !!document.getElementById('canvas'), null, { timeout: 30_000 });
    expect(await win.evaluate(() => new URL(location.href).searchParams.get('e2e'))).toBe(null);
    expect(await win.evaluate(() => (window as any).__gridwellTest)).toBeUndefined();
  } finally {
    await app.close().catch(() => {});
    fs.rmSync(home, { recursive: true, force: true });
  }
});
