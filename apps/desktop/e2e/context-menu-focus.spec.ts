import { test, expect } from './fixtures';

// Right-clicking a live url pane moves focus to it, like every other press.
//
// The rule is "clicks act in the focused pane; a click in an unfocused pane
// moves focus, nothing else", and focusToPane is its one owner. A live url view
// swallows the renderer's own mouse events, so the left button reaches that
// owner through the preload's VIEW_LEFTDOWN relay — but a plain right press is
// deliberately NOT forwarded (urlview-preload only forwards one once it has
// become a drag), so it became a native context menu on a pane that never took
// focus. Picking Reload then reloaded a pane the bar was not even riding.
//
// The fix is at the funnel both doors into that menu pass through:
// WebviewRegistry.showContextMenu announces the pane it is about to open on,
// main relays it as EV.menuPane, and the wasm routes it into the same
// focusToPane. This spec crosses the whole seam — a genuine right press inside
// the view's own webContents, the real menu, the real Reload item — and asserts
// the pane took focus and the bar slid under it.

test('right-clicking a live url pane focuses it before the menu can act', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // A live url descent, on a url the sidecar actually serves so the load
  // commits and the view's getURL carries the marker.
  const marker = 'gwe2emenufocus';
  await gw.openPalette();
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

  // Split it: focus moves to the new pane, the url pane keeps its live view and
  // loses focus. That is the state the bug lives in.
  await gw.splitFocusedPaneVertical();
  const otherPaneId = (await gw.focused()).id;
  expect(otherPaneId, 'the split moved focus off the url pane').not.toBe(urlPaneId);

  // Intercept Menu.popup: a real native popup would never be dismissed under
  // xvfb.
  await electronApp.evaluate(({ Menu }) => {
    const g = globalThis as any;
    g.__gwCtxOrigPopup = Menu.prototype.popup;
    g.__gwCtxMenu = null;
    (Menu.prototype as any).popup = function (this: any) {
      g.__gwCtxMenu = this;
      return undefined;
    };
  });

  try {
    // A genuine right press and release inside the view's own webContents —
    // the events the canvas never sees. The preload does not suppress a plain
    // click, so Chromium emits context-menu and the registry pops the menu.
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
      .poll(() => electronApp.evaluate(() => Boolean((globalThis as any).__gwCtxMenu)), {
        timeout: 10_000,
      })
      .toBe(true);

    // The menu opened, so the pane it acts in already has focus — before any
    // item can run.
    await expect
      .poll(() => gw.focused().then((p) => p.id), { timeout: 5_000 })
      .toBe(urlPaneId);

    // And the one bar slid under it: the bar rides the focused pane.
    const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
    const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
    expect(bar.left, 'the bar rides the newly focused pane').toBeCloseTo(urlPane.x, 0);
    expect(bar.width, 'and spans it').toBeCloseTo(urlPane.w, 0);

    // Joe's case: pick Reload. Focus stays where the right-click put it, so the
    // reload happens in the focused pane.
    const labels = await electronApp.evaluate(() =>
      (globalThis as any).__gwCtxMenu.items.map((i: any) => i.label).filter((l: string) => l),
    );
    expect(labels, 'the in-page menu carries the navigation block').toContain('Reload');
    await electronApp.evaluate(() => {
      const item = (globalThis as any).__gwCtxMenu.items.find((i: any) => i.label === 'Reload');
      if (!item) throw new Error('Reload item missing');
      item.click();
    });
    await gw.waitIdle();
    expect((await gw.focused()).id, 'Reload ran in the pane the right-click focused').toBe(
      urlPaneId,
    );
  } finally {
    await electronApp.evaluate(({ Menu }) => {
      const g = globalThis as any;
      if (g.__gwCtxOrigPopup) (Menu.prototype as any).popup = g.__gwCtxOrigPopup;
      delete g.__gwCtxOrigPopup;
      delete g.__gwCtxMenu;
    });
  }
});
