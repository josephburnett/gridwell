import type { ElectronApplication, Page } from '@playwright/test';
import { test, expect, homePassword, loginToken } from './fixtures';
import { dumpNow } from './trace';

// A right-drag that starts inside a live url view is the pane gesture it is on
// the canvas. The view takes the press and the moves and release after it,
// parked or not, while it can tell where the pointer is; on macOS that ends
// at the view's edge with one mouseLeave, and main reads the cursor from
// there (forwardeddrag.ts). So the drag here is real input in the view's
// webContents: the preview follows it, the gesture lands at the release, the
// page sees no contextmenu and no menu pops.

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

// The canvas pixel at (x, y), as the page painted it.
function pixel(window: Page, x: number, y: number): Promise<number[]> {
  return window.evaluate(
    ([px, py]) => {
      const canvas = document.querySelector('canvas')!;
      const s = canvas.width / canvas.getBoundingClientRect().width;
      const d = canvas.getContext('2d')!.getImageData(Math.round(px * s), Math.round(py * s), 1, 1).data;
      return [d[0], d[1], d[2]];
    },
    [x, y],
  );
}

// Whether the swap arrow (theme.Palette.SwapArrow, a blue at half alpha) is
// painted a quarter of the way along the line between two pane centres, on
// the line or a pixel off it.
async function arrowPainted(window: Page, from: Pt, to: Pt): Promise<boolean> {
  const x = from.x + (to.x - from.x) / 4;
  const y = from.y + (to.y - from.y) / 4;
  for (const dy of [-1, 0, 1]) {
    const [r, g, b] = await pixel(window, x, y + dy);
    if (b - Math.max(r, g) > 50) return true;
  }
  return false;
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
    await expect.poll(() => arrowPainted(window, sfrom, sto), { message: 'the swap arrow is painted' }).toBe(true);
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

// The drag starts in a pane focus has left, so the press's focus and the
// drag's forward both run on it, and once from inside a same-origin iframe
// covering the page, which no top-frame listener hears.
for (const iframe of [false, true]) {
  test(`a right-drag started in an unfocused live url pane${iframe ? ' from inside an iframe' : ''} focuses it and lands`, async ({
    electronApp,
    gw,
    window,
  }) => {
    await gw.enterSource('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy);
    const marker = `gwe2erightdragunfocused${iframe ? 'iframe' : ''}`;
    await gw.openMenu();
    await gw.dragCreate('url', cx, cy);
    await gw.descendCell(cx, cy);
    await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
    await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?${marker}=1`);
    await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
    await expect
      .poll(
        () =>
          electronApp.evaluate(
            ({ webContents }, m) => webContents.getAllWebContents().some((w) => w.getURL().includes(m)),
            marker,
          ),
        { timeout: 15_000 },
      )
      .toBe(true);
    const urlPaneId = (await gw.focused()).id;
    await gw.splitFocusedPaneVertical();
    expect((await gw.focused()).id, 'the split moved focus off the url pane').not.toBe(urlPaneId);
    await electronApp.evaluate(async ({ webContents }, a) => {
      const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(a.marker));
      if (!wc) throw new Error('live view webContents not found');
      if (wc.isLoadingMainFrame()) await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
      if (!a.iframe) return;
      await wc.executeJavaScript(`new Promise((done) => {
        const f = document.createElement('iframe');
        f.style.cssText = 'position:fixed;left:0;top:0;width:100vw;height:100vh;border:0;margin:0';
        f.onload = () => done(true);
        f.srcdoc = '<body style="margin:0;height:100vh">inner</body>';
        document.body.appendChild(f);
      })`);
    }, { marker, iframe });

    const p = (await gw.panes()).find((q) => q.id === urlPaneId)!;
    const from = { x: Math.round(p.x + p.w * 0.15), y: Math.round(p.y + p.h / 2) };
    const to = { x: Math.round(p.x + p.w * 0.6), y: from.y };
    const dragAt = () =>
      window.evaluate(() => {
        const rd = (window as any).__gridwellTest.rightDrag();
        return rd && { x: rd.x, y: rd.y };
      });
    try {
      expect(await rightDragInView(electronApp, marker, urlPaneId, from, to), 'the drag parked the view').toBe(true);
      await expect.poll(() => gw.focused().then((q) => q.id), { message: 'the press focused the url pane' }).toBe(urlPaneId);
      await expect.poll(dragAt, { message: 'the split preview followed the pointer' }).toEqual(to);
      await electronApp.evaluate(() => (globalThis as any).__gwDragRelease());
      await gw.waitIdle();
      expect((await gw.panes()).length, 'the right-drag split the url pane').toBe(3);
    } finally {
      await electronApp.evaluate(() => delete (globalThis as any).__gwDragRelease).catch(() => {});
    }
  });
}

// On macOS the view that took the press says mouseLeave once the pointer is off
// it and then nothing, not even the release, which only the canvas learns from
// its next buttonless move. Main reads the cursor from the leave on, so the
// preview follows the OS pointer, and stops when the page says the gesture
// ended.
test('a right-drag whose view says the pointer left follows the OS cursor until the page ends it', async ({
  electronApp,
  gw,
  home,
  window,
}) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const marker = 'gwe2erightdragleave';
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?${marker}=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  await expect
    .poll(
      () => electronApp.evaluate(({ webContents }, m) => webContents.getAllWebContents().some((w) => w.getURL().includes(m)), marker),
      { timeout: 15_000 },
    )
    .toBe(true);
  await electronApp.evaluate(async ({ webContents }, m) => {
    const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(m));
    if (wc?.isLoadingMainFrame()) await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
  }, marker);
  const dragAt = () =>
    window.evaluate(() => {
      const rd = (window as any).__gridwellTest.rightDrag();
      return rd && { x: rd.x, y: rd.y };
    });
  // The OS cursor in canvas pixels, as main maps it.
  const cursor = (): Promise<Pt> =>
    electronApp.evaluate(({ screen, BaseWindow }) => {
      const c = screen.getCursorScreenPoint();
      const cb = BaseWindow.getAllWindows()[0].getContentBounds();
      return { x: c.x - cb.x, y: c.y - cb.y };
    });

  const p = await gw.focused();
  // The pane's centre, its swap third; one pane, so the release swaps nothing.
  const from = { x: Math.round(p.x + p.w / 2), y: Math.round(p.y + p.h / 2) };
  const os = await cursor();
  // The view's last move is somewhere the OS cursor is not, so only the
  // follow can bring the preview to it.
  const to = { x: from.x + (os.x === from.x + 60 ? 70 : 60), y: from.y + (os.y === from.y + 30 ? 40 : 30) };
  try {
    expect(await rightDragInView(electronApp, marker, p.id, from, to), 'the drag parked the view').toBe(true);
    await expect.poll(dragAt, { message: 'the view told the moves' }).toEqual(to);
    await electronApp.evaluate(({ webContents }, m) => {
      const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(m));
      wc!.sendInputEvent({ type: 'mouseLeave', x: -1, y: -1 } as any);
    }, marker);
    await expect.poll(dragAt, { message: "the preview followed the OS cursor after the view's leave" }).toEqual(await cursor());

    // The page learns the release as macOS tells it: a move on the canvas
    // with no button held.
    await window.mouse.move(from.x, from.y);
    await expect.poll(dragAt, { message: 'the buttonless move ended the drag' }).toBeNull();
    const token = await loginToken(gw.origin, homePassword(home));
    const ended = async () => {
      const lines = (await dumpNow(window, gw.origin, token)).lines;
      const mine = lines.filter((r) => r.origin === 'electron' && r.src === 'webview' && r.kv?.view === p.id);
      return {
        blind: mine.filter((r) => r.kind === 'drag-blind').length,
        end: mine.filter((r) => r.kind === 'drag-end').map((r) => ({ how: r.kv?.how, polled: Number(r.kv?.polled) > 0 })),
      };
    };
    await expect
      .poll(ended, { message: 'main heard the page end the follow, after its clock told a move', timeout: 15_000 })
      .toEqual({ blind: 1, end: [{ how: 'page', polled: true }] });
  } finally {
    await electronApp.evaluate(() => delete (globalThis as any).__gwDragRelease).catch(() => {});
  }
});
