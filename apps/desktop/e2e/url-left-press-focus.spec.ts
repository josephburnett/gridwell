import type { ElectronApplication, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver } from './driver';

// A left press in an unfocused pane's live url view focuses that pane, on any
// page and from any frame of it. The view swallows the renderer's own mouse
// events, so main hears the press on the view's webContents and names the
// pane: a press on a page that cancels pointerdown, one inside an iframe,
// which no top-frame listener hears, and one while a transition animates all
// land.
// The press here is real input in the view's own webContents, which the canvas
// never sees.

// liveUnfocused makes a live url pane on marker and splits focus off it,
// answering the url pane's id and the url tile's cell.
async function liveUnfocused(electronApp: ElectronApplication, gw: GridwellDriver, window: Page, marker: string): Promise<{ urlPaneId: string; cx: number; cy: number }> {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
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
  return { urlPaneId, cx, cy };
}

// pressInView arms a pointerdown counter in the page, or in a same-origin
// iframe covering it, cancelling it when asked, then presses the left button
// at (60, 60) in the view and answers how many pointerdowns the page saw.
function pressInView(
  electronApp: ElectronApplication,
  a: { marker: string; paneId: string; cancels: boolean; iframe: boolean },
): Promise<number> {
  return electronApp.evaluate(async ({ webContents, BaseWindow }, a) => {
    const reg = (globalThis as any).__gwRegistry;
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(a.marker));
    if (!wc) throw new Error('live view webContents not found');
    if (wc.isLoadingMainFrame()) {
      await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
    }
    await wc.executeJavaScript(`(async () => {
      let w = window;
      if (${a.iframe}) {
        const f = document.createElement('iframe');
        f.style.cssText = 'position:fixed;left:0;top:0;width:100vw;height:100vh;border:0;margin:0';
        const loaded = new Promise((r) => (f.onload = r));
        f.srcdoc = '<body style="margin:0;height:100vh">inner</body>';
        document.body.appendChild(f);
        await loaded;
        w = f.contentWindow;
      }
      window.__gwFrame = w;
      w.__gwPointerdowns = 0;
      w.addEventListener('pointerdown', (e) => {
        w.__gwPointerdowns++;
        if (${a.cancels}) e.preventDefault();
      });
      return true;
    })()`);
    const v = reg.viewBoundsFor(a.paneId);
    const cb = BaseWindow.getAllWindows()[0].getContentBounds();
    const at = { x: 60, y: 60, globalX: cb.x + v.x + 60, globalY: cb.y + v.y + 60 };
    wc.sendInputEvent({ type: 'mouseMove', ...at } as any);
    wc.sendInputEvent({ type: 'mouseDown', ...at, button: 'left', clickCount: 1 } as any);
    wc.sendInputEvent({ type: 'mouseUp', ...at, button: 'left', clickCount: 1 } as any);
    await new Promise((r) => setTimeout(r, 100));
    return wc.executeJavaScript('window.__gwFrame.__gwPointerdowns');
  }, a);
}

for (const [label, cancels, iframe] of [
  ['', false, false],
  [' when the page cancels pointerdown', true, false],
  [' from inside an iframe', false, true],
] as const) {
  test(`a left press in an unfocused live url pane focuses it${label}`, async ({ electronApp, gw, window }) => {
    const marker = `gwe2eleftfocus${cancels ? 'cancel' : iframe ? 'iframe' : 'plain'}`;
    const { urlPaneId } = await liveUnfocused(electronApp, gw, window, marker);
    expect(await pressInView(electronApp, { marker, paneId: urlPaneId, cancels, iframe }), 'the press reached the page').toBe(1);
    await expect.poll(() => gw.focused().then((p) => p.id), { timeout: 5_000 }).toBe(urlPaneId);
  });
}

// The other pane descends, its transition stretched long enough to press
// through, and the press in the live view still moves focus.
test('a left press in an unfocused live url pane focuses it while a transition runs', async ({ electronApp, gw, window }) => {
  const marker = 'gwe2eleftfocustrans';
  const { urlPaneId, cx, cy } = await liveUnfocused(electronApp, gw, window, marker);
  // The split ascends the copied place, so the other pane shows the url
  // tile's grid; a click on the tile descends there.
  const tile = await gw.cellCenter((await gw.focused()).id, cx, cy);
  await window.evaluate(() => (window as any).__gridwellTest.setTransitionMs(4000));
  try {
    await window.mouse.click(tile.x, tile.y);
    await expect.poll(() => window.evaluate(() => (window as any).__gridwellTest.transitioning())).toBe(true);
    expect(await pressInView(electronApp, { marker, paneId: urlPaneId, cancels: false, iframe: false }), 'the press reached the page').toBe(1);
    const during = await window.evaluate(() => (window as any).__gridwellTest.transitioning());
    expect(during, 'the press landed while the transition ran').toBe(true);
    await expect.poll(() => gw.focused().then((p) => p.id), { timeout: 1_500 }).toBe(urlPaneId);
  } finally {
    await window.evaluate(() => (window as any).__gridwellTest.setTransitionMs(350));
  }
});
