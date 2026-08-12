import { test as base, Page } from '@playwright/test';
import { spawn } from 'node:child_process';
import * as net from 'node:net';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome, PluginSpec } from '../e2e/fixtures';
import { GridwellDriver } from '../e2e/driver';

// Browser-mode e2e fixtures: the SAME wasm client and Go server as the
// Electron suite, but loaded in plain Chromium with NO Electron shell — the
// degraded phone/tablet client. `gridwell serve` is spawned directly (no
// sidecar), the page is Playwright's ordinary browser page, and the
// GridwellDriver / server oracle are reused verbatim from ../e2e. What this
// suite uniquely proves: the client boots and works with no window.gridwell
// bridge, live-URL affordances degrade visibly (client/caps), and the touch
// gesture layer (client/touchgest) drives the real canvas.

const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
  });
}

type Fixtures = {
  serve: { origin: string; home: string };
  window: Page;
  gw: GridwellDriver;
  // extraPlugins mirrors the Electron fixture's option: plugins seedHome
  // registers beyond the default localdb (e.g. an fs plugin with a root).
  extraPlugins: PluginSpec[];
};

export const test = base.extend<Fixtures>({
  extraPlugins: [[], { option: true }],

  // serve seeds a throwaway home (same `gridwell init` path as the Electron
  // suite) and runs the real server on an ephemeral loopback port.
  serve: async ({ extraPlugins }, use) => {
    const home = seedHome(extraPlugins);
    const port = await freePort();
    const origin = `http://127.0.0.1:${port}`;
    const child = spawn(
      path.join(REPO_ROOT, 'gridwell'),
      ['serve', '--bind', `127.0.0.1:${port}`, '--static', path.join(REPO_ROOT, 'web')],
      {
        env: { ...process.env, GRIDWELL_HOME: home },
        stdio: ['ignore', 'pipe', 'pipe'],
      },
    );
    let output = '';
    child.stdout!.on('data', (d) => (output += d));
    child.stderr!.on('data', (d) => (output += d));

    // Readiness = the static root answers. Poll rather than sleep.
    const deadline = Date.now() + 15_000;
    for (;;) {
      try {
        const res = await fetch(origin + '/');
        if (res.ok) break;
      } catch {
        // not up yet
      }
      if (Date.now() > deadline) {
        child.kill('SIGKILL');
        throw new Error(`gridwell serve did not become ready on ${origin}:\n${output}`);
      }
      await new Promise((r) => setTimeout(r, 100));
    }

    await use({ origin, home });

    child.kill('SIGTERM');
    await new Promise<void>((resolve) => {
      const hard = setTimeout(() => {
        child.kill('SIGKILL');
        resolve();
      }, 3_000);
      child.once('exit', () => {
        clearTimeout(hard);
        resolve();
      });
    });
    fs.rmSync(home, { recursive: true, force: true });
  },

  // window is Playwright's plain browser page pointed at the served client,
  // with the ?e2e=1 introspection hook installed — same contract as the
  // Electron suite's window fixture.
  window: async ({ serve, page }, use) => {
    await page.goto(serve.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(page);
  },

  gw: async ({ window, serve }, use) => {
    await use(new GridwellDriver(window, serve.origin));
  },
});

export { expect } from '@playwright/test';
