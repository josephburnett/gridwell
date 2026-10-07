import { test, expect } from './fixtures';
import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from '../e2e/homes';
import { placeTile } from '../e2e/oracle';
import { buildGoTest } from '../e2e/runtree';

// A box's link to a hey thread on the phone client. The link row carries none
// of its thread's content facts, so the address the bar's new-tab button opens
// is the target's, read through the link (client/contentrow) even though the
// client never listed everything, where the thread lives. The HEY is heyfake's
// made-up one (TestE2EHost), served for this spec's life.

const heyDir = makeRunDir();
let host: ChildProcess | undefined;

test.use({
  extraPlugins: [{ kind: 'hey', name: 'hey', config: { binary: path.join(heyDir, 'hey') } }],
});

test.beforeAll(async () => {
  test.setTimeout(180_000);
  const bin = path.join(heyDir, 'heyfake.test');
  buildGoTest('./internal/plugintest/heyfake', bin);
  host = spawn(bin, ['-test.run', '^TestE2EHost$', '-test.timeout', '0'], {
    env: { ...process.env, HEYFAKE_E2E_DIR: heyDir },
    stdio: ['pipe', 'inherit', 'inherit'],
  });
  const deadline = Date.now() + 30_000;
  while (!fs.existsSync(path.join(heyDir, 'ready'))) {
    if (host.exitCode !== null || Date.now() > deadline) throw new Error('heyfake did not come up');
    await new Promise((r) => setTimeout(r, 100));
  }
});

test.afterAll(async () => {
  host?.stdin?.end();
  host?.kill('SIGTERM');
  fs.rmSync(heyDir, { recursive: true, force: true });
});

test("a box's link opens its thread's page in a new tab", async ({ gw, window }) => {
  const notices: string[] = [];
  window.on('console', (m) => {
    if (m.text().includes('no address yet')) notices.push(m.text());
  });

  await gw.clickPluginSwatch('set aside (hey)');
  const f = await gw.focused();
  await expect
    .poll(async () => ((await gw.getGrid(f.gridID)).tiles ?? []).length, { timeout: 30_000 })
    .toBe(1);
  let link = ((await gw.getGrid(f.gridID)).tiles ?? [])[0] as any;
  expect(link.reference, 'the Set Aside row is a link').toBe(true);
  expect(link.linkTargetId, 'to the thread in everything').toBeTruthy();
  expect(link.servesPage ?? false, 'and carries none of its content facts').toBe(false);

  // The plugin's own placement may be off screen; put the row under the view.
  const [cx, cy] = [Math.round(f.cx), Math.round(f.cy)];
  await placeTile(gw.origin, link.id, link.version, f.gridID, cx, cy, 1, 1);
  await gw.waitClientTileAt(f.id, cx, cy, link.id);
  link = ((await gw.getGrid(f.gridID)).tiles ?? [])[0] as any;

  // Opening the + menu read every collection, everything included, for its
  // swatch. A reload comes back to the box with nothing else read, as a phone
  // returning to the tab does.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.waitClientTileAt(f.id, cx, cy, link.id);

  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(link.id);

  // The button is offered once the target is read, and then opens its page.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.barSlot()), { timeout: 15_000 })
    .toBe('opentab');
  const pal = await gw.palette();
  const [popup] = await Promise.all([
    window.context().waitForEvent('page', { timeout: 10_000 }),
    window.mouse.click(pal.plusX, pal.plusY),
  ]);
  await popup.waitForURL(/\/content\//, { timeout: 10_000 });
  const url = new URL(popup.url());
  expect(url.pathname).toMatch(/^\/content\/[0-9a-f]{64}\//);
  expect(url.pathname.endsWith(`/${link.linkTargetId}/`), `${url.pathname} names the target`).toBe(true);
  const res = await popup.request.get(popup.url());
  expect(res.status()).toBe(200);
  expect(await res.text()).toContain('the kite flew sideways');
  await popup.close();
  expect(notices, 'the tap never says the tile has no address').toEqual([]);
});
