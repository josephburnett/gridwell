import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Drives the text-editing gesture end to end: create a markdown tile, descend
// into it, type, ascend, and assert the typed content reached the server
// (GetTileContent). The edit LOGIC is unit-tested in client/textedit +
// client/markdown; this proves the wiring — keystrokes → store → durable body.
test('typing into a descended text tile persists to the server', async ({ gw, window }) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  // Descend into the tile (enters its text editor) and type. The bar
  // takes the text family's shades (issue #223) — same classifier as the
  // pane border, so band and frame can never disagree.
  await gw.descendCell(cx, cy);
  const themed = await window.evaluate(() => (window as any).__gridwellTest.bar());
  expect(themed.band, 'text-family band').toBe('#1b2213');
  expect(themed.button, 'text-family button').toBe('#8aa05a');
  // The rendered/raw toggle is a DOM element in the same slot; it must wear
  // the same family shades as the canvas buttons (issue #227 — it used to
  // freeze pane-tile teal into its style at creation, a second copy of the
  // theme fact that #223 never reached).
  const toggle = window.locator('#gw-text-toggle');
  await expect(toggle).toBeVisible();
  await expect
    .poll(async () =>
      toggle.evaluate((el: HTMLElement) => getComputedStyle(el).backgroundColor))
    .toBe('rgb(138, 160, 90)'); // #8aa05a, the text-family button hue
  const marker = 'gridwell-e2e-typed';
  await gw.typeText(marker);

  // Ascend back out (bar crumb click), flushing the edit.
  await gw.ascendViaCrumb();

  // The server's stored body now contains what we typed.
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain(marker);
});
