import { test, expect } from './fixtures';

// Issues #131/#132: the url modal is DOM, so a live WebContentsView would
// paint OVER it — the views must park while it is open (and return after),
// and the card keeps a FIXED width so suggestion matches can't make it jump.

test('live views park under the url modal and return on close', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('local');

  // A live view first.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?m=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const viewBounds = () =>
    electronApp.evaluate(({ webContents, BaseWindow }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('m=1'));
      if (!wc) return { x: -55555, y: 0 }; // wc GONE marker

      const win = BaseWindow.getAllWindows()[0];
      const v = (win.contentView.children as unknown as { webContents?: { id: number }; getBounds(): { x: number; y: number } }[]).find(
        (c) => c.webContents?.id === wc.id,
      );
      return v ? v.getBounds() : { x: -77777, y: 0 }; // child gone marker
    });
  // The webContents count increments before the view's URL commits — poll
  // until the finder resolves it on screen.
  await expect
    .poll(async () => (await viewBounds())!.x, { timeout: 10_000 })
    .toBeGreaterThan(-1000);

  // Split, focus the other pane, open the modal there: the live view PARKS.
  await gw.splitFocusedPaneVertical();
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await expect
    .poll(async () => (await viewBounds())!.x, { timeout: 5_000 })
    .toBeLessThan(-1000); // parkedBounds coordinates

  // The card width is FIXED regardless of what's typed (issue #132).
  const cardW = () =>
    window.evaluate(() => document.querySelector('.gw-modal .card')!.getBoundingClientRect().width);
  const w0 = await cardW();
  await window.fill('#gw-url-input', 'a-very-long-url-fragment-that-used-to-stretch-the-card');
  expect(await cardW()).toBeCloseTo(w0, 1);

  // Cancel: the view returns on screen.
  await window.locator('#gw-url-cancel').click();
  await expect
    .poll(async () => (await viewBounds())!.x, { timeout: 5_000 })
    .toBeGreaterThan(-1000);
});
