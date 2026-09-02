import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The bar circle's right-click pops the live url view's context menu, Freeze
// Page included. A page can hijack contextmenu inside the view and make the
// in-page menu unreachable, but the circle sits in the one bar's band, below
// every pane and so outside every view's bounds, so a right-click there
// reaches the canvas whatever the page does. This spec crosses the whole seam: canvas
// right-click, the bottomBarClick slot arm, the bridgeShowMenu IPC,
// registry.showMenu, and the same urlContextMenuTemplate the in-page path uses.
// It then fires the real Freeze Page item and asserts the freeze lands on the
// tile.

test('right-clicking the bar circle over a live url pops the context menu; Freeze Page freezes', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  // A live url descent: create, then prompt on the first descent.
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?circle=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  const live = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('circle=1')),
    );
  await expect.poll(live, { timeout: 15_000 }).toBe(true);

  // Intercept Menu.popup in main, since a native popup would block under xvfb.
  // The captured menu is stashed for the poll below.
  await electronApp.evaluate(({ Menu }) => {
    const g = globalThis as any;
    g.__gwCircleOrigPopup = Menu.prototype.popup;
    g.__gwCircleMenu = null;
    (Menu.prototype as any).popup = function (this: any) {
      g.__gwCircleMenu = this;
      return undefined;
    };
  });

  try {
    // Right-click the circle slot, the bar band's rightmost SlotW: the same
    // point url-freeze-intent.spec.ts clicks for the reconnect button.
    const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
    await window.mouse.click(bar.left + bar.width - 24, bar.top + bar.height / 2, {
      button: 'right',
    });

    await expect
      .poll(() => electronApp.evaluate(() => Boolean((globalThis as any).__gwCircleMenu)), {
        timeout: 10_000,
      })
      .toBe(true);
    const labels = await electronApp.evaluate(() =>
      (globalThis as any).__gwCircleMenu.items
        .map((i: any) => i.label)
        .filter((l: string) => l),
    );
    expect(labels, 'the circle menu must offer Freeze Page (a durable tile)').toContain(
      'Freeze Page',
    );
    expect(labels, 'the circle menu carries the navigation block').toContain('Reload');

    // Fire the real Freeze Page item: the live view tears down and the standing
    // frozen intent persists on the tile.
    await electronApp.evaluate(() => {
      const m = (globalThis as any).__gwCircleMenu;
      const item = m.items.find((i: any) => i.label === 'Freeze Page');
      if (!item) throw new Error('Freeze Page item missing');
      item.click();
    });
    await expect.poll(live, { timeout: 15_000 }).toBe(false);
    await expect
      .poll(async () => {
        const t = tileAt(await gw.getGrid(grid), 'url', cx, cy);
        return Boolean(t?.urlFrozen);
      }, { timeout: 10_000 })
      .toBe(true);
  } finally {
    await electronApp.evaluate(({ Menu }) => {
      const g = globalThis as any;
      if (g.__gwCircleOrigPopup) (Menu.prototype as any).popup = g.__gwCircleOrigPopup;
      delete g.__gwCircleOrigPopup;
      delete g.__gwCircleMenu;
    });
  }

  await gw.middleClickCell(cx, cy); // teardown ascent
});
