import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// Crosses the browser-mode seam: the full core loop — boot, enter a plugin,
// create a tile, descend, type, persist — must work with NO Electron shell
// and no window.gridwell bridge, against the same server the desktop uses.
// The errors() hook at the end is the "nothing degraded silently" oracle.
test('the plain-browser client boots, creates, and edits', async ({ gw, window }) => {
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created from a plain browser').toBeTruthy();

  await gw.descendCell(cx, cy);
  await gw.typeText('written from a plain browser');
  await gw.ascendViaCrumb(); // ascend flushes the text save
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('written from a plain browser');

  // No notice on the strip: browser mode must not be quietly erroring its
  // way through the core loop.
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices).toEqual([]);
});
