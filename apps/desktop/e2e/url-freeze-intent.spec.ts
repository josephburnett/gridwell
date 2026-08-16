import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #237: the explicit freeze gesture stores the USER'S intent on the
// tile. Unlike the transient navigate-away freeze (which auto-revives on
// return, #202), a deliberately frozen url stays frozen across re-descent
// — until the reconnect button clears the intent by going live. This
// drives the whole seam: the renderer entry the context menu's "Freeze
// Page" fires (the native menu itself cannot be driven headlessly; its
// template/action binding is unit-tested in contextmenu.test.ts), the
// SetTile url_frozen arm, the localdb column, and DecideAutoLive.

test('freeze intent survives re-descent; reconnect clears it', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  // A live url descent (the #209 create-then-prompt flow).
  await gw.openPalette();
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

  // The freeze gesture: send the exact IPC the "Freeze Page" menu item
  // fires, at the pane holding the live view.
  const paneId = (await gw.focused()).id;
  await electronApp.evaluate(({ BrowserWindow }, pid: string) => {
    BrowserWindow.getAllWindows()[0].webContents.send('gw:freeze-url', { paneId: pid });
  }, paneId);

  // The live view tears down and the intent persists (framing: no bump is
  // pinned by the localdb unit tests; here we pin the stored fact).
  await expect.poll(live, { timeout: 15_000 }).toBe(false);
  await expect
    .poll(async () => {
      const t = tileAt(await gw.getGrid(grid), 'url', cx, cy);
      return Boolean(t?.urlFrozen);
    }, { timeout: 10_000 })
    .toBe(true);

  // Ascend, then re-descend: a user-frozen url must NOT auto-go-live —
  // the exact opposite of auto-live.spec.ts's #202 behavior.
  await gw.middleClickCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).not.toBe('');
  await window.waitForTimeout(1_500); // give a wrong auto-live time to fire
  expect(await live(), 'a deliberately frozen url stays frozen on descent').toBe(false);

  // The bar slot shows the reconnect circle; clicking it goes live AND
  // clears the standing intent (going live IS the unfreeze).
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
