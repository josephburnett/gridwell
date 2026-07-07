import { test as base, _electron as electron, ElectronApplication, Page } from '@playwright/test';
import { execFileSync, spawnSync } from 'node:child_process';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';
import { GridwellDriver } from './driver';
import { pluginUUIDs, killTmuxServers } from './homes';

// apps/desktop, and the repo root two levels up (where `make build` lays out the
// gridwell sidecar + plugin binaries and the web/ static dir).
const DESKTOP_DIR = path.resolve(__dirname, '..');
const REPO_ROOT = path.resolve(DESKTOP_DIR, '..', '..');

// PluginSpec is one extra plugin to register via `gridwell init` beyond the
// default localdb (see seedHome's extra param) — e.g. an fs plugin with no
// config.root, the rootless-launcher-tile case (issue #47).
export interface PluginSpec {
  kind: string;
  name: string;
  config?: Record<string, string>;
}

// seedHome creates a throwaway Gridwell home and registers one localdb plugin in
// it via `gridwell init` — the same coordinated setup a real user runs. server.yaml
// is mandatory (no fallback), so every launch points GRIDWELL_HOME at a home seeded
// this way. `extra` registers additional plugins (in order, after the localdb) the
// same way, for tests that need a second plugin present at boot. Returns the home
// dir; callers remove it on teardown.
export function seedHome(extra: PluginSpec[] = []): string {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-e2e-'));
  const bin = path.join(REPO_ROOT, 'gridwell');
  const env = { ...process.env, GRIDWELL_HOME: home };
  execFileSync(bin, ['init', '--kind', 'localdb', '--name', 'e2e'], { env, stdio: 'ignore' });
  for (const p of extra) {
    const args = ['init', '--kind', p.kind, '--name', p.name];
    for (const [k, v] of Object.entries(p.config ?? {})) args.push('--config', `${k}=${v}`);
    execFileSync(bin, args, { env, stdio: 'ignore' });
  }
  return home;
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
  // extraPlugins is a test option (set via test.use({ extraPlugins: [...] })
  // in a spec file, e.g. plugin-health.spec.ts): plugins seedHome registers
  // beyond the default localdb, present from the very first launch.
  extraPlugins: PluginSpec[];
};

// The e2e fixture launches the SAME `electron .` entry that `make launch` uses
// (apps/desktop/src/main/index.ts spawns the Go sidecar itself), so the whole
// stack — renderer → wasm → Connect-RPC → server → SQLite — is exercised. Each
// test gets a fresh temp home seeded with one localdb plugin; GRIDWELL_E2E=1
// turns on the renderer's read-only introspection hook.
//
// Isolation guarantee: GRIDWELL_HOME is a per-test mkdtemp. Electron's userData
// is set to <home>/electron via TWO mechanisms that work in concert:
//
//   1. --user-data-dir command-line flag: Chromium reads this BEFORE Node.js
//      modules run, so Playwright's loader.js interception of app.isReady cannot
//      delay it. This is the reliable path for e2e isolation.
//
//   2. applyUserDataOverride in index.ts: belt for direct (non-Playwright) app
//      launches where GRIDWELL_HOME is set but --user-data-dir is not passed.
//
// With both in place, no test instance shares ~/.config/gridwell-desktop with
// the live app or with concurrent test instances.
//
// Teardown: after app.close() the fixture kills stray tmux servers and asserts
// the sidecar exited so any leak is blamed on the test that caused it, not the
// next one.
export const test = base.extend<Fixtures>({
  extraPlugins: [[], { option: true }],

  electronApp: async ({ extraPlugins }, use) => {
    const home = seedHome(extraPlugins);
    // The per-test Chromium profile. Created before launch so the flag is valid.
    const electronDir = path.join(home, 'electron');
    fs.mkdirSync(electronDir, { recursive: true });
    const app = await electron.launch({
      // Pass --user-data-dir as a Chromium/Electron command-line switch so
      // Chromium picks up the isolated profile directory BEFORE the Node.js main
      // script (index.js) runs. This is necessary because Playwright's loader.js
      // intercepts app.isReady(), making app.setPath() in index.ts unreliable
      // for Chromium profile isolation (Chromium has already initialised by then).
      //
      // IMPORTANT: the flag must come BEFORE the app path ('.') in args.
      // Electron treats everything after the app-path argument as app arguments
      // rather than Electron/Chromium switches. Playwright prepends --inspect=0
      // and --remote-debugging-port=0 at the front, so the final argv looks like:
      //   electron --inspect=0 --remote-debugging-port=0 --user-data-dir=... .
      // after Playwright's loader.js splice, which is the correct order.
      args: [`--user-data-dir=${electronDir}`, '.'],
      cwd: DESKTOP_DIR,
      env: {
        // Strip live-app plugin env vars so they cannot bleed into the test
        // sidecar's plugin subprocess. GRIDWELL_PLUGIN_CONFIG carries the live
        // app's DB path; if it reaches the go-plugin Start() call it ends up as
        // the LAST duplicate in the subprocess env (go-plugin re-appends
        // os.Environ()) and overrides the fresh per-test config. GRIDWELL_PLUGIN
        // is a companion var set by go-plugin itself in the live app's tmux
        // session. Both are reset correctly by the sidecar for each fresh launch.
        ...Object.fromEntries(
          Object.entries(process.env).filter(
            ([k]) => k !== 'GRIDWELL_PLUGIN_CONFIG' && k !== 'GRIDWELL_PLUGIN',
          ),
        ),
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
