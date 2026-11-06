import { test, expect } from './fixtures';

// Right-clicking a live url pane focuses it, as any press in a pane does,
// though the click itself is the page's. A live url view swallows the
// renderer's mouse events and urlview-preload forwards a right press only once
// it has become a drag, so the focus cannot come from the canvas: the registry
// sees the press on the view's webContents, main relays it as EV.rightPress,
// and the wasm routes it into focusToPane, the one owner of "a click in an
// unfocused pane moves focus". The spec crosses that seam with a real right
// press inside the view's own webContents.

test('right-clicking a live url pane focuses it and pops no menu', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // The url has to be one the sidecar serves, so the load commits and the
  // view's getURL carries the marker.
  const marker = 'gwe2emenufocus';
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?${marker}=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  const live = () =>
    electronApp.evaluate(
      ({ webContents }, m) => webContents.getAllWebContents().some((w) => w.getURL().includes(m)),
      marker,
    );
  await expect.poll(live, { timeout: 15_000 }).toBe(true);

  const urlPaneId = (await gw.focused()).id;

  // The split leaves the url pane live but unfocused.
  await gw.splitFocusedPaneVertical();
  const otherPaneId = (await gw.focused()).id;
  expect(otherPaneId, 'the split moved focus off the url pane').not.toBe(urlPaneId);

  await electronApp.evaluate(({ Menu }) => {
    const g = globalThis as any;
    g.__gwFocusOrigPopup = Menu.prototype.popup;
    g.__gwFocusPopups = 0;
    (Menu.prototype as any).popup = function () {
      g.__gwFocusPopups++;
    };
  });

  try {
    // A right press inside the view's own webContents, which the canvas never
    // sees.
    await electronApp.evaluate(async ({ webContents }, m) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(m));
      if (!wc) throw new Error('live view webContents not found');
      if (wc.isLoadingMainFrame()) {
        await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
      }
      wc.sendInputEvent({ type: 'mouseDown', x: 60, y: 60, button: 'right', clickCount: 1 } as any);
      wc.sendInputEvent({ type: 'mouseUp', x: 60, y: 60, button: 'right', clickCount: 1 } as any);
    }, marker);

    await expect
      .poll(() => gw.focused().then((p) => p.id), { timeout: 5_000 })
      .toBe(urlPaneId);

    // The bar rides the focused pane.
    const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
    const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
    expect(bar.left, 'the bar rides the newly focused pane').toBeCloseTo(urlPane.x, 0);
    expect(bar.width, 'and spans it').toBeCloseTo(urlPane.w, 0);
    expect(
      await electronApp.evaluate(() => (globalThis as any).__gwFocusPopups),
      'Gridwell popped a menu over the page',
    ).toBe(0);
  } finally {
    await electronApp.evaluate(({ Menu }) => {
      const g = globalThis as any;
      if (g.__gwFocusOrigPopup) (Menu.prototype as any).popup = g.__gwFocusOrigPopup;
      delete g.__gwFocusOrigPopup;
      delete g.__gwFocusPopups;
    });
  }
});
