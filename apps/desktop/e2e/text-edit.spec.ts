import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Drives the text-editing gesture end to end: create a markdown tile, descend
// into it, type, ascend, and assert the typed content reached the server through
// GetTileContent. The edit logic is unit-tested in client/textedit and
// client/markdown; this proves the wiring from keystrokes to a durable body.
test('typing into a descended text tile persists to the server', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  // Descend into the tile, which enters its text editor, and type. The bar takes
  // the text family's shades from the same classifier as the pane border, so the
  // band and the frame cannot disagree.
  await gw.descendCell(cx, cy);
  const themed = await window.evaluate(() => (window as any).__gridwellTest.bar());
  expect(themed.band, 'text-family band').toBe('#1b2213');
  expect(themed.button, 'text-family button').toBe('#8aa05a');
  // The rendered and raw toggle is a DOM element in the same slot, and it must
  // wear the same family shades as the canvas buttons. Baking a color into its
  // style at creation would be a second copy of the theme fact.
  const toggle = window.locator('#gw-text-toggle');
  await expect(toggle).toBeVisible();
  await expect
    .poll(async () =>
      toggle.evaluate((el: HTMLElement) => getComputedStyle(el).backgroundColor))
    .toBe('rgb(138, 160, 90)'); // #8aa05a, the text-family button hue
  const marker = 'gridwell-e2e-typed';
  await gw.typeText(marker);

  // Ascend back out with a bar crumb click, flushing the edit.
  await gw.ascendViaCrumb();

  // The server's stored body now contains what was typed.
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain(marker);
});
