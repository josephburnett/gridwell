import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A url tile's face is its preview, fetched on every draw until it lands. A
// preview the server refuses is the same answer every time, so it is asked
// for once; without a failure latch each refusal's notice paints the frame
// that asks again. unreadable-body.spec.ts is the same rule for a body.

test('a preview the server refuses is asked for once', async ({ electronApp, gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // The ascent out of the live page freezes it, which mints the preview.
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?up=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  const live = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('up=1')),
    );
  await expect.poll(live, { timeout: 15_000 }).toBe(true);
  await gw.middleClickCell(cx, cy);
  await expect.poll(live, { timeout: 15_000 }).toBe(false);
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)?.previewBlobId ?? 0), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  const tile = tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)!;
  await gw.waitIdle();

  let refused = 0;
  await window.route('**/gridwell.v1.Gridwell/GetTilePreview', (r: any) => {
    if (!(r.request().postData() ?? '').includes(tile.id)) return r.continue();
    refused++;
    return r.fulfill({
      status: 404,
      contentType: 'application/json',
      body: JSON.stringify({ code: 'not_found', message: `plugin: no tile ${tile.id}` }),
    });
  });

  // A reload empties the preview cache, so the first draw of the tile asks.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(f.gridID);
  await expect.poll(() => refused, { timeout: 10_000 }).toBeGreaterThan(0);

  // Two seconds of the tile on screen is over a hundred frames if each
  // refusal draws the next.
  await window.waitForTimeout(2_000);
  const asked = refused;
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  await window.unroute('**/gridwell.v1.Gridwell/GetTilePreview');
  expect(asked, 'GetTilePreview calls for the refused preview').toBe(1);
  expect(
    e.notices.filter((n: any) => n.source === 'rpc:GetTilePreview').length,
    'the strip holds one notice for it',
  ).toBe(1);
});
