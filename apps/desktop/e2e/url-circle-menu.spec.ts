import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The bar circle's right-click pops the live url pane's menu, Copy URL and
// Freeze Page included. It is the one menu over a live url pane, since a
// right-click on the page is the page's. The seam runs from the canvas right-click through
// the bottomBarClick slot arm, the bridgeShowMenu IPC, registry.showMenu, and
// urlContextMenuTemplate.

test('right-clicking the bar circle over a live url pops the context menu; Copy URL copies, Freeze Page freezes', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  // Drag-create lands the tile bare, so the first descent prompts.
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?circle=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  // The live view's address, if one shows tag; '' if none does.
  const live = (tag: string) =>
    electronApp.evaluate(
      ({ webContents }, tag) => webContents.getAllWebContents().find((w) => w.getURL().includes(tag))?.getURL() ?? '',
      tag,
    );
  await expect.poll(() => live('circle=1'), { timeout: 15_000 }).not.toBe('');

  // A native popup would block under xvfb, so Menu.popup is intercepted and the
  // menu stashed for the poll below.
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
    // The circle slot is the bar band's rightmost SlotW, the same point
    // url-freeze-intent.spec.ts clicks for the reconnect button.
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
    expect(labels[0], 'Copy URL is the first row').toBe('Copy URL');

    // The page moves after the menu opened, as a followed link does, and Copy
    // URL copies where it is when the row is chosen.
    await electronApp.evaluate(({ webContents }) => {
      const w = webContents.getAllWebContents().find((w) => w.getURL().includes('circle=1'));
      void w!.executeJavaScript(`location.href = location.href.replace('circle=1', 'circle=2')`);
    });
    await expect.poll(() => live('circle=2'), { timeout: 15_000 }).not.toBe('');
    await electronApp.evaluate(({ clipboard }) => clipboard.writeText(''));
    await electronApp.evaluate(() => {
      (globalThis as any).__gwCircleMenu.items.find((i: any) => i.label === 'Copy URL').click();
    });
    expect(await electronApp.evaluate(({ clipboard }) => clipboard.readText())).toBe(await live('circle=2'));

    // Freeze Page tears the live view down and persists the frozen intent on
    // the tile.
    await electronApp.evaluate(() => {
      const m = (globalThis as any).__gwCircleMenu;
      const item = m.items.find((i: any) => i.label === 'Freeze Page');
      if (!item) throw new Error('Freeze Page item missing');
      item.click();
    });
    await expect.poll(() => live('circle=2'), { timeout: 15_000 }).toBe('');
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
