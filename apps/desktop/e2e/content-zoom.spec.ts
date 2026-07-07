import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #82: Ctrl/Cmd +/-/0 zooms a descended tile's CONTENT. The zoom is
// per-tile FRAMING — persisted server-side (content_zoom), never bumping the
// version — so a zoomed doc comes back at your size on every descent.

test('Ctrl+= zooms a text tile: persisted as framing, no version bump', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // Zoom in three steps: 1.1^3 ≈ 1.331, persisted on the tile.
  for (let i = 0; i < 3; i++) await window.keyboard.press('Control+=');
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)?.contentZoom ?? 0), {
      timeout: 10_000,
    })
    .toBeCloseTo(1.331, 2);

  // Framing, not content: the version did not move.
  const after = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  expect(after.version, 'no version bump from zooming').toBe(created.version);

  // Ctrl+0 resets.
  await window.keyboard.press('Control+0');
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)?.contentZoom ?? 0))
    .toBeCloseTo(1.0, 3);
});

test('Ctrl+= zooms a live url view (composed with the layout zoom)', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?zoom=82`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const factorOf = () =>
    electronApp.evaluate(({ webContents }) => {
      const zs = webContents
        .getAllWebContents()
        .map((wc) => {
          try {
            return wc.getURL().includes('zoom=82') ? wc.getZoomFactor() : 0;
          } catch {
            return 0;
          }
        })
        .filter((z) => z > 0);
      return zs[0] ?? 0;
    });

  await expect.poll(factorOf, { timeout: 10_000 }).toBeGreaterThan(0);
  const base = await factorOf();
  for (let i = 0; i < 3; i++) await window.keyboard.press('Control+=');
  await expect
    .poll(factorOf, { timeout: 10_000 })
    .toBeCloseTo(Math.min(base * 1.331, 3), 1);
});
