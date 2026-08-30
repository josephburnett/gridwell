import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A new text tile must be independent and empty, never seeded with a
// previously-edited tile's body. Asserted at the store through GetTileContent,
// the system of record.
test('a newly created text tile is empty, not the previously edited content', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Tile A: create, descend, type a distinctive body, ascend to flush.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const a = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(a, 'first text tile created').toBeTruthy();
  await gw.descendCell(cx, cy);
  await gw.typeText('first-tile-content');
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => gw.getTileContent(a.id), { timeout: 10_000 })
    .toContain('first-tile-content');

  // Tile B: a second text tile at a different cell.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 1, cy);
  const b = tileAt(await gw.getGrid(grid), 'text', cx + 1, cy)!;
  expect(b, 'second text tile created').toBeTruthy();
  expect(b.id, 'B is a distinct tile').not.toBe(a.id);

  // B must be empty; it must not inherit A's content.
  const bContent = await gw.getTileContent(b.id);
  expect(bContent, 'a new text tile is empty, not the last-edited content').toBe('');
});
