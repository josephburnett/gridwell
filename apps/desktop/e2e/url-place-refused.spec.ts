import { test, expect } from './fixtures';

// Every bridge verb is an ipcMain.handle on the other side, so every one of
// them can reject. A dropped promise on `place` is the worst of them: the wasm
// sets the pane's live handle BEFORE main answers, so a refused placement
// leaves a pane the renderer believes is live with nothing on screen, nothing
// said, and — because placeURLView returns early for the tile already live in
// the pane — no way back to live short of ascending out.
//
// The registry is exposed to the spec under GRIDWELL_E2E, so the refusal is
// injected at the real seam: main's own handler throws.

async function notices(window: any): Promise<string[]> {
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  return e.notices.map((n: any) => `${n.source}|${n.message}`);
}

test('a refused place surfaces and leaves the pane retryable, not falsely live', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // Main refuses every placement, the way a destroyed window or a view that
  // will not attach does.
  await electronApp.evaluate(() => {
    const g = globalThis as any;
    const reg = g.__gwRegistry;
    if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');
    g.__gwRealPlace = reg.place.bind(reg);
    reg.place = async () => {
      throw new Error('e2e: place refused');
    };
  });

  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?refused=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  const liveWithMarker = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('refused=1')),
    );

  // The refusal reaches the strip, on the same source main's own webview
  // failures report under.
  await expect
    .poll(async () => (await notices(window)).join('\n'), {
      message: 'a rejected bridge call must surface, never log and return',
      timeout: 10_000,
    })
    .toContain('placeWebview failed');
  expect(await liveWithMarker(), 'main made no view, so nothing is live').toBe(false);

  // With main answering again, the bar circle's retry goes live. A live handle
  // left standing from the refused place would make placeURLView return early
  // — the pane already shows this tile — and the retry would do nothing at all.
  await electronApp.evaluate(() => {
    const g = globalThis as any;
    g.__gwRegistry.place = g.__gwRealPlace;
  });
  const slot = await window.evaluate(() => (window as any).__gridwellTest.palette());
  await window.mouse.click(slot.plusX, slot.plusY);
  await expect
    .poll(liveWithMarker, {
      message: 'the pane must still be able to go live after a refused place',
      timeout: 20_000,
    })
    .toBe(true);
});
