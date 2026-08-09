import { test as base, expect, Page } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import * as net from 'node:net';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { GridwellDriver } from '../e2e/driver';

// server.yaml `disable_shells: true`, seen from a real client: the +
// palette offers every primitive EXCEPT shell. The flag rides the
// ListPlugins handshake into caps (the one owner of "what can this client
// do"), and the same server refuses shell creates outright — the palette
// gap is the UI face of a server-side refusal, not a client preference.
// The no-flag suites (web-core & co., browser-shim in the Electron suite)
// pin the default: shell present.

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
  serve: { origin: string };
  window: Page;
  gw: GridwellDriver;
};

const test = base.extend<Fixtures>({
  serve: async ({}, use) => {
    const home = seedHome();
    fs.appendFileSync(path.join(home, 'server.yaml'), 'disable_shells: true\n');
    const port = await freePort();
    const origin = `http://127.0.0.1:${port}`;
    const child: ChildProcess = spawn(
      path.join(REPO_ROOT, 'gridwell'),
      ['serve', '--bind', `127.0.0.1:${port}`, '--static', path.join(REPO_ROOT, 'web')],
      { env: { ...process.env, GRIDWELL_HOME: home }, stdio: ['ignore', 'pipe', 'pipe'] },
    );
    let output = '';
    child.stdout!.on('data', (d) => (output += d));
    child.stderr!.on('data', (d) => (output += d));
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

    await use({ origin });

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

  window: async ({ serve, page }, use) => {
    await page.goto(serve.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(page);
  },

  gw: async ({ window, serve }, use) => {
    await use(new GridwellDriver(window, serve.origin));
  },
});

test('disable_shells removes the shell primitive from the + palette', async ({ gw }) => {
  await gw.enterPlugin('e2e'); // the seeded localdb — a writable grid, so primitives show
  await gw.openPalette();
  const pal = await gw.palette();
  const primitives = pal.items.filter((i: any) => !i.isPlugin).map((i: any) => i.kind);
  expect(primitives, 'the other primitives all survive').toEqual(
    expect.arrayContaining(['well', 'markdown', 'url', 'pane']),
  );
  expect(primitives, 'the shell swatch must be gone').not.toContain('shell');
});

export { expect };
