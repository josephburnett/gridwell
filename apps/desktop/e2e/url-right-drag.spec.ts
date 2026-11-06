import { test, expect } from './fixtures';

// A right-drag that starts inside a live url view is the pane gesture it is on
// the canvas. The view takes the press, so urlview-preload watches it, and
// once it is a drag main forwards the press point and the renderer parks the
// view; the rest of the drag then lands on the canvas. The press and the first
// moves are real input in the view's webContents, the rest real input on the
// canvas with the right button still held, and the page sees no contextmenu
// and no menu pops.

test('a right-drag started inside a live url view splits the pane', async ({ electronApp, gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  const marker = 'gwe2erightdrag';
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

  const p = await gw.focused();
  expect((await gw.panes()).length).toBe(1);
  // The pane's left third, inside the view: outside the swap third and the
  // resize band, so the press classifies as a split.
  const from = { x: Math.round(p.x + p.w * 0.15), y: Math.round(p.y + p.h / 2) };
  const to = { x: Math.round(p.x + p.w * 0.5), y: from.y };

  await electronApp.evaluate(({ Menu }) => {
    const g = globalThis as any;
    g.__gwDragOrigPopup = Menu.prototype.popup;
    g.__gwDragPopups = 0;
    (Menu.prototype as any).popup = function () {
      g.__gwDragPopups++;
    };
  });
  try {
    const parked = await electronApp.evaluate(
      async ({ webContents, BaseWindow }, a) => {
        const reg = (globalThis as any).__gwRegistry;
        const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(a.marker));
        if (!wc) throw new Error('live view webContents not found');
        if (wc.isLoadingMainFrame()) {
          await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
        }
        await wc.executeJavaScript(
          `window.__gwMenus = 0; document.addEventListener('contextmenu', () => window.__gwMenus++)`,
        );
        const b = reg.viewBoundsFor(a.paneId);
        const cb = BaseWindow.getAllWindows()[0].getContentBounds();
        // The preload measures the drag in screen pixels, which main maps back
        // through the window's content bounds.
        const at = (dx: number) => {
          const x = a.from.x - b.x + dx;
          const y = a.from.y - b.y;
          return { x, y, globalX: cb.x + b.x + x, globalY: cb.y + b.y + y };
        };
        wc.sendInputEvent({ type: 'mouseMove', ...at(0) } as any);
        wc.sendInputEvent({ type: 'mouseDown', ...at(0), button: 'right', clickCount: 1 } as any);
        // Past the far threshold, so the drag needs no hold.
        for (let i = 1; i <= 8; i++) {
          wc.sendInputEvent({ type: 'mouseMove', ...at(i * 5), modifiers: ['rightButtonDown'] } as any);
          await new Promise((r) => setTimeout(r, 10));
        }
        const by = Date.now() + 8_000;
        while (Date.now() < by) {
          if (reg.viewBoundsFor(a.paneId).x !== b.x) return true;
          await new Promise((r) => setTimeout(r, 25));
        }
        return false;
      },
      { marker, paneId: p.id, from },
    );
    expect(parked, 'the drag parked the view, so the renderer took the forwarded press').toBe(true);

    // Playwright's mouse would send these moves with no button held, which
    // reads as a lost release.
    const cdp = await window.context().newCDPSession(window);
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: to.x, y: to.y, button: 'right', buttons: 2 });
    await cdp.send('Input.dispatchMouseEvent', {
      type: 'mouseReleased',
      x: to.x,
      y: to.y,
      button: 'right',
      buttons: 0,
      clickCount: 1,
    });
    await gw.waitIdle();
    expect((await gw.panes()).length, 'the right-drag split the pane').toBe(2);

    const seen = await electronApp.evaluate(async ({ webContents }, m) => {
      const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(m));
      return {
        page: wc ? await wc.executeJavaScript('window.__gwMenus') : -1,
        popups: (globalThis as any).__gwDragPopups,
      };
    }, marker);
    expect(seen.page, 'the page saw a contextmenu for a drag').toBe(0);
    expect(seen.popups, 'a menu popped for a drag').toBe(0);
  } finally {
    await electronApp.evaluate(({ Menu }) => {
      const g = globalThis as any;
      if (g.__gwDragOrigPopup) (Menu.prototype as any).popup = g.__gwDragOrigPopup;
      delete g.__gwDragOrigPopup;
      delete g.__gwDragPopups;
    });
  }
});
