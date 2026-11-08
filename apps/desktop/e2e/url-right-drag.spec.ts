import type { ElectronApplication } from '@playwright/test';
import { test, expect } from './fixtures';

// A right-drag that starts inside a live url view is the pane gesture it is on
// the canvas. The view takes the press and, as every OS does with a held
// button, every move and the release after it, parked or not. So the whole
// drag here is real input in the view's webContents: the preview follows it,
// the gesture lands at the release, the page sees no contextmenu and no menu
// pops.

type Pt = { x: number; y: number };

// rightDragInView presses the right button at from in the pane's live view and
// drags it to to, all in the view's webContents, leaving the button held. It
// answers whether the view parked, which says the renderer took the forward.
function rightDragInView(app: ElectronApplication, marker: string, paneId: string, from: Pt, to: Pt): Promise<boolean> {
  return app.evaluate(
    async ({ webContents, BaseWindow }, a) => {
      const reg = (globalThis as any).__gwRegistry;
      const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(a.marker));
      if (!wc) throw new Error('live view webContents not found');
      const b = reg.viewBoundsFor(a.paneId);
      const cb = BaseWindow.getAllWindows()[0].getContentBounds();
      // Relative to where the view is now, parked or not, as the OS reports a
      // held button to the view that took the press; main maps the screen
      // point back through the window's content bounds.
      const at = (p: { x: number; y: number }) => {
        const v = reg.viewBoundsFor(a.paneId);
        return { x: p.x - v.x, y: p.y - v.y, globalX: cb.x + p.x, globalY: cb.y + p.y };
      };
      const held = { modifiers: ['rightButtonDown'] };
      wc.sendInputEvent({ type: 'mouseMove', ...at(a.from) } as any);
      wc.sendInputEvent({ type: 'mouseDown', ...at(a.from), button: 'right', clickCount: 1 } as any);
      // Past the far threshold, so the drag needs no hold.
      for (let i = 1; i <= 8; i++) {
        wc.sendInputEvent({ type: 'mouseMove', ...at({ x: a.from.x + i * 5, y: a.from.y }), ...held } as any);
        await new Promise((r) => setTimeout(r, 10));
      }
      let parked = false;
      for (const by = Date.now() + 8_000; !parked && Date.now() < by; ) {
        parked = reg.viewBoundsFor(a.paneId).x !== b.x;
        if (!parked) await new Promise((r) => setTimeout(r, 25));
      }
      for (let i = 1; i <= 8; i++) {
        const p = { x: a.from.x + 40 + ((a.to.x - a.from.x - 40) * i) / 8, y: a.from.y + ((a.to.y - a.from.y) * i) / 8 };
        wc.sendInputEvent({ type: 'mouseMove', ...at(p), ...held } as any);
        await new Promise((r) => setTimeout(r, 10));
      }
      (globalThis as any).__gwDragRelease = () =>
        wc.sendInputEvent({ type: 'mouseUp', ...at(a.to), button: 'right', clickCount: 1 } as any);
      return parked;
    },
    { marker, paneId, from, to },
  );
}

test('a right-drag started inside a live url view previews and lands where it is released', async ({ electronApp, gw, window }) => {
  await gw.enterSource('home');
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
  const urlTile = (await gw.focused()).textFocus;

  await electronApp.evaluate(async ({ webContents, Menu }, m) => {
    const g = globalThis as any;
    g.__gwDragOrigPopup = Menu.prototype.popup;
    g.__gwDragPopups = 0;
    (Menu.prototype as any).popup = function () {
      g.__gwDragPopups++;
    };
    const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(m));
    if (!wc) throw new Error('live view webContents not found');
    if (wc.isLoadingMainFrame()) await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
    await wc.executeJavaScript(`window.__gwMenus = 0; document.addEventListener('contextmenu', () => window.__gwMenus++)`);
  }, marker);
  const dragAt = () =>
    window.evaluate(() => {
      const rd = (window as any).__gridwellTest.rightDrag();
      return rd && { x: rd.x, y: rd.y };
    });
  const release = () => electronApp.evaluate(() => (globalThis as any).__gwDragRelease());
  try {
    const p = await gw.focused();
    expect((await gw.panes()).length).toBe(1);
    // The pane's left third, inside the view: outside the swap third and the
    // resize band, so the press classifies as a split.
    const from = { x: Math.round(p.x + p.w * 0.15), y: Math.round(p.y + p.h / 2) };
    const to = { x: Math.round(p.x + p.w * 0.6), y: from.y };
    expect(await rightDragInView(electronApp, marker, p.id, from, to), 'the drag parked the view').toBe(true);
    await expect.poll(dragAt, { message: 'the split preview followed the pointer' }).toEqual(to);
    await release();
    await gw.waitIdle();
    const panes = await gw.panes();
    expect(panes.length, 'the right-drag split the pane').toBe(2);
    expect(
      panes.some((q) => Math.abs(q.x - to.x) <= 2 || Math.abs(q.x + q.w - to.x) <= 2),
      `the split landed at the release, ${to.x}: ${JSON.stringify(panes)}`,
    ).toBe(true);

    // The inner third of the live view's pane swaps it with the pane the
    // release lands on.
    const src = panes.find((q) => q.textFocus === urlTile)!;
    const dst = panes.find((q) => q.id !== src.id)!;
    const sfrom = { x: Math.round(src.x + src.w / 2), y: Math.round(src.y + src.h / 2) };
    const sto = { x: Math.round(dst.x + dst.w / 2), y: Math.round(dst.y + dst.h / 2) };
    await expect.poll(live, { timeout: 15_000 }).toBe(true);
    expect(await rightDragInView(electronApp, marker, src.id, sfrom, sto), 'the drag parked the view').toBe(true);
    await expect.poll(dragAt, { message: 'the swap preview names the pane under the pointer' }).toEqual(sto);
    await release();
    await gw.waitIdle();
    const after = await gw.panes();
    const landed = after.find((q) => q.x <= sto.x && sto.x < q.x + q.w && q.y <= sto.y && sto.y < q.y + q.h);
    expect(landed?.textFocus, 'the swap moved the live tile to the release pane').toBe(urlTile);

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
      delete g.__gwDragRelease;
    });
  }
});
