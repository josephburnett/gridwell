import { test, expect } from './fixtures';

// A left press in an unfocused pane's live url view focuses that pane, on any
// page. The view swallows the renderer's own mouse events, so the focus comes
// from urlview-preload's forward. A page that cancels pointerdown, as drag
// and editor libraries do, makes Chromium skip the mousedown that follows, so
// a forward that listened for mousedown never ran there. The press here is
// real input in the view's own webContents, which the canvas never sees.

for (const cancels of [false, true]) {
  test(`a left press in an unfocused live url pane focuses it${cancels ? ' when the page cancels pointerdown' : ''}`, async ({
    electronApp,
    gw,
    window,
  }) => {
    await gw.enterSource('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy);

    const marker = `gwe2eleftfocus${cancels ? 'cancel' : 'plain'}`;
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

    await gw.splitFocusedPaneVertical();
    expect((await gw.focused()).id, 'the split moved focus off the url pane').not.toBe(urlPaneId);

    const pageSawPress = await electronApp.evaluate(
      async ({ webContents, BaseWindow }, a) => {
        const reg = (globalThis as any).__gwRegistry;
        const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(a.marker));
        if (!wc) throw new Error('live view webContents not found');
        if (wc.isLoadingMainFrame()) {
          await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
        }
        await wc.executeJavaScript(`
          window.__gwPointerdowns = 0;
          window.addEventListener('pointerdown', (e) => {
            window.__gwPointerdowns++;
            if (${a.cancels}) e.preventDefault();
          });
          true`);
        const v = reg.viewBoundsFor(a.paneId);
        const cb = BaseWindow.getAllWindows()[0].getContentBounds();
        const at = { x: 60, y: 60, globalX: cb.x + v.x + 60, globalY: cb.y + v.y + 60 };
        wc.sendInputEvent({ type: 'mouseMove', ...at } as any);
        wc.sendInputEvent({ type: 'mouseDown', ...at, button: 'left', clickCount: 1 } as any);
        wc.sendInputEvent({ type: 'mouseUp', ...at, button: 'left', clickCount: 1 } as any);
        await new Promise((r) => setTimeout(r, 100));
        return wc.executeJavaScript('window.__gwPointerdowns');
      },
      { marker, paneId: urlPaneId, cancels },
    );
    expect(pageSawPress, 'the press reached the page').toBe(1);

    await expect
      .poll(() => gw.focused().then((p) => p.id), { timeout: 5_000 })
      .toBe(urlPaneId);
  });
}
