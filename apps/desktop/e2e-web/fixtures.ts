import { test as base, Page } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { seedHome, PluginSpec } from '../e2e/fixtures';
import { serveBin, staticDir, treeEnv } from '../e2e/runtree';
import { GridwellDriver } from '../e2e/driver';
import { setOracleAuth } from '../e2e/oracle';
import { parseServingLine } from '../src/main/lines';
import { freePort } from '../src/main/freeport';

// The same wasm client and Go server as the Electron suite, in plain Chromium
// with no Electron shell, which is the phone and tablet client. This suite
// alone sees the client booting with no window.gridwell bridge, the live-url
// affordances degrading through client/caps, and client/touchgest on the real
// canvas. GridwellDriver and the oracle are reused verbatim from ../e2e.

// The sidecar's own electron-free picker.
export { freePort };

type Fixtures = {
  serve: Served;
  window: Page;
  gw: GridwellDriver;
  // Mirrors the Electron fixture's option: plugins seedHome registers beyond
  // the node's own home.
  extraPlugins: PluginSpec[];
};

// One running `gridwell serve`. token is what a logged-in browser's cookie
// carries.
export interface Served {
  origin: string;
  home: string;
  token: string;
  child: ChildProcess;
}

// The one serve spawner for the browser suites. The banner is readiness,
// because the server prints "serving on" only once both doors listen.
export async function spawnServe(home: string, port: number, extraArgs: string[] = []): Promise<Served> {
  const origin = `http://127.0.0.1:${port}`;
  const child = spawn(
    serveBin(),
    ['serve', '--bind', `127.0.0.1:${port}`, '--static', staticDir(), ...extraArgs],
    { env: { ...process.env, ...treeEnv(), GRIDWELL_HOME: home }, stdio: ['ignore', 'pipe', 'pipe'] },
  );
  let output = '';
  child.stdout!.on('data', (d) => (output += d));
  child.stderr!.on('data', (d) => (output += d));
  const deadline = Date.now() + 15_000;
  for (;;) {
    // The sidecar's own reader in lines.ts, so a banner change that breaks the
    // app breaks this suite the same way rather than passing a private regex.
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

// SIGTERM with a SIGKILL fallback, resolving once the process has exited.
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

// The cookie a spec's own fetch must carry against a served origin.
export function authHeaders(served: Served): Record<string, string> {
  return { Cookie: `gridwell_auth=${served.token}` };
}

// Seeds the page's cookie jar, so it boots straight into the client the way a
// returning browser does. web-auth.spec.ts alone drives the login form.
export async function authenticate(page: Page, served: Served): Promise<void> {
  await page.context().addCookies([{ name: 'gridwell_auth', value: served.token, url: served.origin }]);
}

export const test = base.extend<Fixtures>({
  extraPlugins: [[], { option: true }],

  // A throwaway home, as the Electron suite seeds one, on an ephemeral port.
  serve: async ({ extraPlugins }, use) => {
    const home = seedHome(extraPlugins);
    const served = await spawnServe(home, await freePort());
    await use(served);
    await stopServe(served.child);
    fs.rmSync(home, { recursive: true, force: true });
  },

  // A plain browser page with the ?e2e=1 hook installed, the same contract as
  // the Electron suite's window fixture.
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
