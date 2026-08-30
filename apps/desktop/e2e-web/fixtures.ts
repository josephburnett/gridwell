import { test as base, Page } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome, PluginSpec } from '../e2e/fixtures';
import { GridwellDriver } from '../e2e/driver';
import { setOracleAuth } from '../e2e/oracle';
import { parseServingLine } from '../src/main/lines';
import { freePort } from '../src/main/freeport';

// Browser-mode e2e fixtures: the same wasm client and Go server as the Electron
// suite, loaded in plain Chromium with no Electron shell, which is the degraded
// phone and tablet client. `gridwell serve` is spawned directly, with no
// sidecar, the page is Playwright's ordinary browser page, and GridwellDriver
// and the server oracle are reused verbatim from ../e2e. What this suite alone
// proves: the client boots and works with no window.gridwell bridge, live-url
// affordances degrade visibly through client/caps, and the touch gesture layer
// in client/touchgest drives the real canvas.

const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');

// serveBin picks the server binary: the stock one, or what GRIDWELL_SERVE_BIN
// names.
export const serveBin = () => path.join(REPO_ROOT, process.env.GRIDWELL_SERVE_BIN || 'gridwell');

// freePort is the sidecar's own electron-free picker; there is one copy.
export { freePort };

type Fixtures = {
  serve: Served;
  window: Page;
  gw: GridwellDriver;
  // extraPlugins mirrors the Electron fixture's option: plugins seedHome
  // registers beyond the node's own home, such as an fs plugin with a root.
  extraPlugins: PluginSpec[];
};

// Served is one running `gridwell serve`: its web origin, its home, and the auth
// token the banner announced. The web door is always gated by the password serve
// minted, and the token is what a logged-in browser's cookie carries.
export interface Served {
  origin: string;
  home: string;
  token: string;
  child: ChildProcess;
}

// spawnServe is the one serve spawner for the browser suites: it boots a node on
// a home, waits for the banner, and returns the origin with its token. Readiness
// is the banner itself, since the server prints "serving on" only once both
// doors listen.
export async function spawnServe(home: string, port: number, extraArgs: string[] = []): Promise<Served> {
  const origin = `http://127.0.0.1:${port}`;
  const child = spawn(
    serveBin(),
    ['serve', '--bind', `127.0.0.1:${port}`, '--static', path.join(REPO_ROOT, 'web'), ...extraArgs],
    { env: { ...process.env, GRIDWELL_HOME: home }, stdio: ['ignore', 'pipe', 'pipe'] },
  );
  let output = '';
  child.stdout!.on('data', (d) => (output += d));
  child.stderr!.on('data', (d) => (output += d));
  const deadline = Date.now() + 15_000;
  for (;;) {
    // The banner is parsed by the sidecar's own reader in lines.ts, the one boot
    // contract with `gridwell serve`, so a banner change that breaks the app
    // breaks this suite the same way rather than passing a private regex.
    const served = output.split('\n').map(parseServingLine).find((a) => a?.auth);
    if (served?.auth) {
      setOracleAuth(origin, served.auth); // every served node is reachable by the oracle
      return { origin, home, token: served.auth, child };
    }
    if (child.exitCode !== null || Date.now() > deadline) {
      child.kill('SIGKILL');
      throw new Error(`gridwell serve did not announce on ${origin}:\n${output}`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
}

// stopServe sends SIGTERM with a SIGKILL fallback and resolves once the process
// has exited.
export async function stopServe(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null) return;
  await new Promise<void>((resolve) => {
    const hard = setTimeout(() => {
      child.kill('SIGKILL');
      resolve();
    }, 3_000);
    child.once('exit', () => {
      clearTimeout(hard);
      resolve();
    });
    child.kill('SIGTERM');
  });
}

// authHeaders is the cookie a spec's own fetch must carry against a served
// origin.
export function authHeaders(served: Served): Record<string, string> {
  return { Cookie: `gridwell_auth=${served.token}` };
}

// authenticate seeds the page's cookie jar with a served node's token, so the
// page boots straight into the client, the way a returning browser does.
// web-auth.spec.ts alone drives the login form.
export async function authenticate(page: Page, served: Served): Promise<void> {
  await page.context().addCookies([{ name: 'gridwell_auth', value: served.token, url: served.origin }]);
}

export const test = base.extend<Fixtures>({
  extraPlugins: [[], { option: true }],

  // serve seeds a throwaway home, the same way the Electron suite does, and runs
  // the real server on an ephemeral loopback port.
  serve: async ({ extraPlugins }, use) => {
    const home = seedHome(extraPlugins);
    const served = await spawnServe(home, await freePort());
    await use(served);
    await stopServe(served.child);
    fs.rmSync(home, { recursive: true, force: true });
  },

  // window is Playwright's plain browser page pointed at the served client, with
  // the ?e2e=1 introspection hook installed: the same contract as the Electron
  // suite's window fixture.
  window: async ({ serve, page }, use) => {
    await authenticate(page, serve);
    await page.goto(serve.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(page);
  },

  gw: async ({ window, serve }, use) => {
    await use(new GridwellDriver(window, serve.origin));
  },
});

export { expect } from '@playwright/test';
