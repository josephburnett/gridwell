import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The bar circle's right-click pops the live url view's context menu
// (Freeze Page included). Motivation: a page can hijack contextmenu inside
// the view and make the in-page menu unreachable — but the circle sits in
// the bar band, carved out of the view's bounds (panebox.BarInset), so a
// right-click there reaches the canvas no matter what the page does. This
// spec crosses the whole seam: canvas right-click → bottomBarClick slot
// arm → bridgeShowMenu IPC → registry.showMenu → the same
// urlContextMenuTemplate the in-page path uses — then fires the real
// Freeze Page item and asserts the freeze lands on the tile.

test('right-clicking the bar circle over a live url pops the context menu; Freeze Page freezes', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  // A live url descent (the #209 create-then-prompt flow).
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

  // Intercept Menu.popup in main (a native popup would block under xvfb);
  // the captured menu is stashed for the poll below.
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
    // Right-click the circle slot: the bar band's rightmost SlotW, same
    // point the reconnect-button click in url-freeze-intent.spec.ts uses.
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

    // Fire the real Freeze Page item: the live view tears down and the
    // standing frozen intent persists on the tile (issue #237 wiring).
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
