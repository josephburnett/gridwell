import { test as base, expect, Page } from '@playwright/test';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { Served, spawnServe, stopServe, freePort, authenticate } from './fixtures';
import { GridwellDriver } from '../e2e/driver';

// server.yaml `disable_shells: true`, seen from a real client: the +
// palette offers every primitive EXCEPT shell. The flag rides the
// Handshake handshake into caps (the one owner of "what can this client
// do"), and the same server refuses shell creates outright — the palette
// gap is the UI face of a server-side refusal, not a client preference.
// The no-flag suites (web-core & co., browser-shim in the Electron suite)
// pin the default: shell present.



type Fixtures = {
  serve: Served;
  window: Page;
  gw: GridwellDriver;
};

const test = base.extend<Fixtures>({
  serve: async ({}, use) => {
    const home = seedHome();
    fs.appendFileSync(path.join(home, 'server.yaml'), 'disable_shells: true\n');
    const served = await spawnServe(home, await freePort());
    await use(served);
    await stopServe(served.child);
    fs.rmSync(home, { recursive: true, force: true });
  },

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

test('disable_shells removes the shell primitive from the + palette', async ({ gw }) => {
  await gw.enterPlugin('home'); // home — a writable grid, so primitives show
  await gw.openPalette();
  const pal = await gw.palette();
  const primitives = pal.items.filter((i: any) => !i.isPlugin).map((i: any) => i.kind);
  expect(primitives, 'the other primitives all survive').toEqual(
    expect.arrayContaining(['well', 'markdown', 'url', 'pane']),
  );
  expect(primitives, 'the shell swatch must be gone').not.toContain('shell');
});

export { expect };
