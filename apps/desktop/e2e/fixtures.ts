import { test as base, _electron as electron, ElectronApplication, Page } from '@playwright/test';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { spawn, ChildProcess } from 'node:child_process';
import { GridwellDriver } from './driver';
import { setOracleAuth } from './oracle';
import { makeHome, homeEnv, removeHome } from './homes';
import { serveBin, treeEnv } from './runtree';
import { parseServingLine } from '../src/main/lines';
import { freePort } from '../src/main/freeport';

// apps/desktop, the app directory `electron .` launches. The artifacts a launch
// runs come from the run's own snapshot, never from the checkout beside it
// (runtree.ts).
const DESKTOP_DIR = path.resolve(__dirname, '..');

// One directory to declare under server.yaml's `files:`. Its swatch in the +
// menu is "<label> (files)" (door.GridName).
export interface FilesRoot {
  root: string;
  label: string;
}

// A throwaway node directory: a server.yaml declaring the given files roots,
// whose first serve mints the ids and creates the store, as a real user's
// first run does. `extraYaml` appends raw sections such as a connections:
// list. Callers remove the returned dir on teardown.
export function seedHome(roots: FilesRoot[] = [], extraYaml = ''): string {
  const home = makeHome();
  let yaml = '';
  if (roots.length) {
    yaml += 'files:\n';
    for (const r of roots) yaml += `    - root: ${JSON.stringify(r.root)}\n      label: ${JSON.stringify(r.label)}\n`;
  }
  fs.writeFileSync(path.join(home, 'server.yaml'), yaml + extraYaml);
  return home;
}

// A second `gridwell serve` a spec reaches through a direct connection. A node
// has exactly one home, so another writable space is another node.
export interface FarNode {
  label: string;
  home: string;
  child: ChildProcess;
}

// Boots a fresh node on a loopback port and waits for its banner. Its
// connection socket lives under its home, which the local node dials.
async function spawnFarNode(label: string): Promise<FarNode> {
  const home = makeHome();
  fs.writeFileSync(path.join(home, 'server.yaml'), '');
  const port = await freePort();
  const child = spawn(serveBin(), ['serve', '--bind', `127.0.0.1:${port}`], {
    env: { ...process.env, ...treeEnv(), ...homeEnv(home) },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stdout!.on('data', (d) => (output += d));
  child.stderr!.on('data', (d) => (output += d));
  const deadline = Date.now() + 15_000;
  for (;;) {
    if (output.split('\n').some((l) => parseServingLine(l)?.auth)) return { label, home, child };
    if (child.exitCode !== null || Date.now() > deadline) {
      child.kill('SIGKILL');
      throw new Error(`far node ${label} did not announce:\n${output}`);
    }
    await new Promise((r) => setTimeout(r, 50));
  }
}

async function stopFarNode(n: FarNode): Promise<void> {
  if (n.child.exitCode === null) {
    await new Promise<void>((resolve) => {
      const hard = setTimeout(() => {
        n.child.kill('SIGKILL');
        resolve();
      }, 3_000);
      n.child.once('exit', () => {
        clearTimeout(hard);
        resolve();
      });
      n.child.kill('SIGTERM');
    });
  }
  removeHome(n.home);
}


// The web password serve minted into the node directory. The file exists only once a
// serve has started there.
export function homePassword(home: string): string {
  return fs.readFileSync(path.join(home, 'web-password'), 'utf8').trim();
}

// The auth cookie the login form issues, the same token the serve banner
// carries, so oracle RPCs and a plain browser page can authenticate.
export async function loginToken(origin: string, password: string): Promise<string> {
  const res = await fetch(origin + '/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ password }).toString(),
    redirect: 'manual',
  });
  const m = /gridwell_auth=([0-9a-f]{64})/.exec(res.headers.get('set-cookie') ?? '');
  if (!m) throw new Error(`login at ${origin} issued no cookie (${res.status})`);
  return m[1];
}

// Blames a leaked sidecar on the test that caused it, rather than on a later
// one hitting a stale port or database lock.
async function assertSidecarExited(pid: number | null): Promise<void> {
  if (pid == null) return;

  // before-quit SIGTERMs the sidecar; 3 s to exit cleanly.
  const deadline = Date.now() + 3_000;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0); // throws ESRCH when the process is gone
    } catch (err: unknown) {
      if ((err as NodeJS.ErrnoException).code === 'ESRCH') return; // gone
      return; // EPERM: it exists but is not ours to signal; treat as fine
    }
    await new Promise((r) => setTimeout(r, 100));
  }

  throw new Error(
    `e2e teardown leak: sidecar (pid ${pid}) still running after app.close(). ` +
      'This test did not clean up properly (e.g. a live shell tile was left open). ' +
      'The sidecar must exit before the next test starts or port/DB locks will bleed.',
  );
}

type Fixtures = {
  // The seeded temp node directory the app launches on; the gw fixture reads its minted
  // web-password to authenticate the oracle.
  home: string;
  electronApp: ElectronApplication;
  window: Page;
  gw: GridwellDriver;
  // Set with test.use({ filesRoots: [...] }): the roots seedHome declares,
  // present from the first launch.
  filesRoots: FilesRoot[];
  // Far nodes to boot and connect directly, one row each in the + menu: the
  // second writable space a cross-namespace spec needs.
  extraNodes: string[];
  extraYaml: string;
  // Set with test.use({ forceDeviceScaleFactor: 2 }) for a window at a Retina
  // display's pixel density; 0 keeps the display's own.
  forceDeviceScaleFactor: number;
  // Set with test.use({ fontconfig: path }) to launch on that fontconfig
  // file; '' keeps the host's.
  fontconfig: string;
};

// The fixture launches the same `electron .` entry `make launch` uses, so the
// whole stack runs: renderer, wasm, Connect-RPC, server, SQLite. Each test gets
// a fresh temp node directory, and GRIDWELL_E2E=1 turns on the renderer's read-only
// introspection hook. Electron's userData is set to ~/.gridwell/electron both by the
// --user-data-dir flag and by applyUserDataOverride, so no test instance shares
// ~/.config/gridwell-desktop with the live app or a concurrent instance.
export const test = base.extend<Fixtures>({
  filesRoots: [[], { option: true }],
  extraNodes: [[], { option: true }],
  extraYaml: ['', { option: true }],
  forceDeviceScaleFactor: [0, { option: true }],
  fontconfig: ['', { option: true }],

  home: async ({ filesRoots, extraNodes, extraYaml }, use) => {
    // Far nodes come up before the local node, so its boot-time connect learns
    // each landing synchronously. They go down after the app closes, because
    // this fixture's teardown runs after electronApp's.
    const far: FarNode[] = [];
    for (const label of extraNodes) far.push(await spawnFarNode(label));
    let yaml = extraYaml;
    if (far.length) {
      yaml += 'connections:\n';
      for (const n of far) {
        yaml += `    - name: ${n.label}\n      label: ${n.label}\n      addr: ${path.join(n.home, 'federation.sock')}\n`;
      }
    }
    await use(seedHome(filesRoots, yaml));
    for (const n of far) await stopFarNode(n);
  },

  electronApp: async ({ home, forceDeviceScaleFactor, fontconfig }, use) => {
    // Created before launch, so the flag below is valid.
    const electronDir = path.join(home, 'electron');
    fs.mkdirSync(electronDir, { recursive: true });
    const app = await electron.launch({
      // A Chromium switch, so the isolated profile is picked up before the
      // Node.js main script runs; Playwright intercepts app.isReady(), so
      // index.ts's setPath would come too late. The flag must precede the app
      // path ('.'), because Electron treats everything after it as app args.
      args: [
        `--user-data-dir=${electronDir}`,
        ...(forceDeviceScaleFactor ? [`--force-device-scale-factor=${forceDeviceScaleFactor}`] : []),
        '.',
      ],
      cwd: DESKTOP_DIR,
      env: {
        ...process.env,
        GRIDWELL_E2E: '1',
        ...(fontconfig ? { FONTCONFIG_FILE: fontconfig } : {}),
        ...homeEnv(home),
        ...treeEnv(),
      },
    });
    await use(app);

    // ── Teardown (runs after every test, pass or fail) ──────────────────────

    // Teardown must complete from any spec end state, including a spec that
    // died with a live shell attached. If it hangs, the worker is SIGKILLed at
    // the test timeout, every cleanup below is skipped, and the report gains an
    // unattributed error that reads as a flake.

    // index.ts exposes the pid under GRIDWELL_E2E=1; null if boot never
    // finished.
    let sidecarPid: number | null = null;
    try {
      sidecarPid = await app.evaluate(
        () => (globalThis as { __gwSidecarPid?: number }).__gwSidecarPid ?? null,
      );
    } catch {
      // The app already crashed or closed; the pid is unknown.
    }

    // close() does not settle when a live shell stream existed at close time:
    // the Electron process exits with code 0 while the Playwright-side promise
    // hangs. So close() races a deadline and the exit is verified here.
    // A spec that ended by quitting the app leaves no process handle to read,
    // and teardown must still finish: that is the whole point of this block.
    let proc: ChildProcess | null = null;
    try {
      proc = app.process();
    } catch {
      // Already exited.
    }
    const closed = await Promise.race([
      app.close().then(
        () => true,
        () => true,
      ),
      new Promise<boolean>((r) => {
        const t = setTimeout(() => r(false), 10_000);
        t.unref?.();
      }),
    ]);
    if (!closed) {
      // Expected only with a live shell, so elsewhere it is new information.
      console.warn('[e2e teardown] electronApp.close() did not settle in 10s; proceeding with direct cleanup');
      if (proc && proc.exitCode === null) {
        // Still alive, rather than the wedge where it has already exited. The
        // sidecar would otherwise never receive before-quit's SIGTERM.
        proc.kill('SIGKILL');
        if (sidecarPid != null) {
          try {
            process.kill(sidecarPid, 'SIGKILL');
          } catch {
            // already gone
          }
        }
      }
    }

    // After the sidecar is gone, so no shell reconnect can start a server the
    // kill would miss.
    await assertSidecarExited(sidecarPid);

    removeHome(home);
  },

  window: async ({ electronApp }, use) => {
    const win = await electronApp.firstWindow();
    // CI sets GRIDWELL_E2E_VERBOSE=1 to mirror the renderer console and the
    // main and sidecar stdio into the worker's stdout, because the trace
    // records gestures and never the console.
    if (process.env.GRIDWELL_E2E_VERBOSE === '1') {
      win.on('console', (msg) => console.log(`[renderer:${msg.type()}] ${msg.text()}`));
      win.on('pageerror', (err) => console.log(`[renderer:pageerror] ${err.message}`));
      const proc = electronApp.process();
      proc.stdout?.on('data', (d: Buffer) => process.stdout.write(`[main] ${d}`));
      proc.stderr?.on('data', (d: Buffer) => process.stdout.write(`[main:err] ${d}`));
    }
    // The sidecar reports ready, then the wasm boots and installs the hook.
    await win.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    // Boot is not done at hook-install: the focused pane's anchor resolves
    // through Handshake and then HomeGrid, and a first focused() read can catch
    // anchor="" and compare against nothing. Ready means anchored.
    await win.waitForFunction(
      () => {
        const t = (window as any).__gridwellTest;
        try {
          return (t.panes() as Array<{ focused: boolean; anchor: string }>).some(
            (p) => p.focused && p.anchor !== '',
          );
        } catch {
          return false;
        }
      },
      null,
      { timeout: 30_000 },
    );
    await use(win);
  },

  gw: async ({ window, home }, use) => {
    const origin = new URL(window.url()).origin;
    setOracleAuth(origin, await loginToken(origin, homePassword(home)));
    await use(new GridwellDriver(window, origin));
  },
});

// So specs import expect from './fixtures' without a second import.
export { expect } from '@playwright/test';
