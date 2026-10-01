import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A url tile's address follows where the user went: a navigation inside the
// live view writes the address it settles on as content, claiming a version
// like a typed one, without waiting for a freeze or an ascent. The tile then
// reopens there after a reload, and coming back to the address already stored
// writes nothing.

test('a navigation inside a live url tile is the tile’s address', async ({ electronApp, window, gw }) => {
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?h=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  const liveURL = () =>
    electronApp.evaluate(({ webContents }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
      return wc ? { url: wc.getURL(), loading: wc.isLoading() } : { url: '', loading: true };
    });
  await expect.poll(liveURL, { timeout: 15_000 }).toMatchObject({ loading: false });
  expect((await liveURL()).url).toContain('h=1');

  const row = async () => tileAt(await gw.getGrid(home.gridID), 'url', cx, cy)!;
  const typed = await row();
  expect(String(typed.urlString), 'the typed address').toContain('h=1');

  // Landing on the typed address wrote nothing: the settle has passed and the
  // row is the one the prompt wrote.
  await window.waitForTimeout(c.urlAddressMs * 2);
  expect(Number((await row()).version), 'opening the page claims nothing').toBe(Number(typed.version));

  await electronApp.evaluate(async ({ webContents }, target: string) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
    if (!wc) throw new Error('live view not found');
    await wc.executeJavaScript(`location.href = ${JSON.stringify(target)}`);
  }, `${gw.origin}/wasm_exec.js?h=2`);
  await expect.poll(async () => (await liveURL()).url, { timeout: 15_000 }).toContain('h=2');

  // Still live: no freeze and no ascent has run, so the settle wrote it.
  await expect
    .poll(async () => String((await row()).urlString), {
      message: 'the landed address reaches the row while the view is live',
      timeout: c.urlAddressMs * 20,
    })
    .toContain('h=2');
  const landed = await row();
  expect(Number(landed.version), 'the landed address is content and bumps').toBeGreaterThan(Number(typed.version));
  expect((await liveURL()).url, 'the view is still live on the second page').toContain('h=2');

  // The tile reopens where the user left it after a reload.
  await gw.middleClickCell(cx, cy);
  await gw.waitIdle();
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect
    .poll(async () => (await gw.panes()).some((p) => p.gridID === home.gridID), { timeout: 30_000 })
    .toBe(true);
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().filter((w) => w.getURL().includes('h=')).length), { timeout: 15_000 })
    .toBe(0);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  await expect.poll(liveURL, { timeout: 15_000 }).toMatchObject({ loading: false });
  expect((await liveURL()).url, 'the tile reopens on the second page').toContain('h=2');

  // Reopening on the stored address is a read: the row is byte-identical.
  await window.waitForTimeout(c.urlAddressMs * 2);
  await gw.waitIdle();
  const reopened = await row();
  expect(String(reopened.urlString)).toBe(String(landed.urlString));
  expect(Number(reopened.version), 'reopening writes nothing').toBe(Number(landed.version));
});
