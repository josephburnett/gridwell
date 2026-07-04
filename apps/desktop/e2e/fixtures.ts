import { test as base, _electron as electron, ElectronApplication, Page } from '@playwright/test';
import { execFileSync, spawnSync } from 'node:child_process';
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

// pluginUUIDs extracts the plugin UUIDs from <home>/server.yaml. Used by
// teardown to kill the per-plugin tmux servers.
function pluginUUIDs(home: string): string[] {
  const p = path.join(home, 'server.yaml');
  let src: string;
  try {
    src = fs.readFileSync(p, 'utf-8');
  } catch {
    return [];
  }
  const uuids: string[] = [];
  for (const line of src.split('\n')) {
    // YAML line: `  id: <uuid>`
    const m = line.match(/^\s*id:\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\s*$/i);
    if (m) uuids.push(m[1]);
  }
  return uuids;
}

// killTmuxServers kills the gridwell-private tmux server for each plugin UUID.
// Each localdb plugin owns a tmux server on socket "gridwell-<uuid>"; if a
// test crashes before deleting shell tiles those sessions linger and can
// interfere with the next test (same socket name if the UUID is reused, or
// just leaked resources). This is a best-effort kill: no-op if the server
// is not running.
function killTmuxServers(uuids: string[]): void {
  for (const uuid of uuids) {
    spawnSync('tmux', ['-L', `gridwell-${uuid}`, 'kill-server'], { stdio: 'ignore' });
  }
}

// assertSidecarExited polls (briefly) that the sidecar process is no longer
// alive after app.close(). Fails loudly so the LEAKING test is blamed, not a
// later one that encounters a stale port or database lock.
async function assertSidecarExited(pid: number | null): Promise<void> {
  if (pid == null) return;

  // Electron's before-quit handler sends SIGTERM to the sidecar; give it up
  // to 3 s to exit cleanly.
  const deadline = Date.now() + 3_000;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0); // throws ESRCH when the process is gone
    } catch (err: unknown) {
      if ((err as NodeJS.ErrnoException).code === 'ESRCH') return; // gone — good
      return; // EPERM means it exists but we can't signal it; assume OK
    }
    await new Promise((r) => setTimeout(r, 100));
  }

  // Still alive after the grace period — fail loudly so the current test
  // is attributed, not a future one that collides with the stale process.
  throw new Error(
    `e2e teardown leak: sidecar (pid ${pid}) still running after app.close(). ` +
      'This test did not clean up properly (e.g. a live shell tile was left open). ' +
      'The sidecar must exit before the next test starts or port/DB locks will bleed.',
  );
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
//
// Isolation guarantee: GRIDWELL_HOME is a per-test mkdtemp; Electron's userData
// is redirected to <home>/electron (by index.ts → applyUserDataOverride), so
// each test gets its own Chromium profile — no shared lock with the live app or
// other concurrent instances.
//
// Teardown: after app.close() the fixture kills stray tmux servers and asserts
// the sidecar exited so any leak is blamed on the test that caused it, not the
// next one.
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

    // ── Teardown (runs after every test, pass or fail) ──────────────────────

    // Capture the sidecar PID before closing (exposed by index.ts under
    // GRIDWELL_E2E=1; null if the app never finished booting).
    let sidecarPid: number | null = null;
    try {
      sidecarPid = await app.evaluate(
        () => (globalThis as { __gwSidecarPid?: number }).__gwSidecarPid ?? null,
      );
    } catch {
      // app already crashed/closed; PID unknown.
    }

    await app.close().catch(() => {});

    // Kill stray tmux servers before removing the home dir — the tmux socket
    // lives in the OS tmpdir (/tmp/tmux-<uid>/gridwell-<uuid>), not under home,
    // so rmSync would not clean it up.
    killTmuxServers(pluginUUIDs(home));

    // Assert the sidecar exited; fail loudly here (this test's teardown) rather
    // than silently polluting the next test.
    await assertSidecarExited(sidecarPid);

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

// Re-export expect so specs can import from './fixtures' without a second import.
export { expect } from '@playwright/test';
