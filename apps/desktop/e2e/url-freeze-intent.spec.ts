import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The explicit freeze gesture stores the user's intent on the tile. The
// transient navigate-away freeze revives on return, while a deliberately frozen
// url stays frozen across re-descent until the reconnect button clears the
// intent by going live. This drives the seam: the renderer entry the context
// menu's "Freeze Page" fires, the SetTile url_frozen arm, the stored column, and
// DecideAutoLive. The native menu cannot be driven headlessly, so its template
// and action binding are unit-tested in contextmenu.test.ts.

test('freeze intent survives re-descent; reconnect clears it', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  // A bare url tile prompts for its address on the first descent.
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?fz=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  const live = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('fz=1')),
    );
  await expect.poll(live, { timeout: 15_000 }).toBe(true);

  // The exact IPC the "Freeze Page" menu item fires, at the pane holding the
  // live view.
  const paneId = (await gw.focused()).id;
  await electronApp.evaluate(({ BrowserWindow }, pid: string) => {
    BrowserWindow.getAllWindows()[0].webContents.send('gw:freeze-url', { paneId: pid });
  }, paneId);

  // The live view tears down and the intent persists. The intent is framing, so
  // it bumps no version, which the store's unit tests pin.
  await expect.poll(live, { timeout: 15_000 }).toBe(false);
  await expect
    .poll(async () => {
      const t = tileAt(await gw.getGrid(grid), 'url', cx, cy);
      return Boolean(t?.urlFrozen);
    }, { timeout: 10_000 })
    .toBe(true);

  // On re-descent a user-frozen url must not go live, where an unfrozen one
  // does; auto-live.spec.ts pins that half.
  await gw.middleClickCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).not.toBe('');
  await window.waitForTimeout(1_500); // give a wrong auto-live time to fire
  expect(await live(), 'a deliberately frozen url stays frozen on descent').toBe(false);

  // The bar slot shows the reconnect circle. Clicking it goes live, and going
  // live is what clears the standing intent.
  const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
  await window.mouse.click(bar.left + bar.width - 24, bar.top + bar.height / 2);
  await expect.poll(live, { timeout: 15_000 }).toBe(true);
  await expect
    .poll(async () => {
      const t = tileAt(await gw.getGrid(grid), 'url', cx, cy);
      return Boolean(t?.urlFrozen);
    }, { timeout: 10_000 })
    .toBe(false);

  await gw.middleClickCell(cx, cy); // teardown ascent
});
