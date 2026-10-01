import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';

// A write whose answer and event carry the change reads nothing back. Its
// TileChanged lands the row in every open view, this one included, so a GetGrid
// behind it is a listing read for a fact the client already holds. The
// cross-node clone is federation.spec.ts's, which has the second node.

test('a clone reads no listing back; its event lands the copy', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.waitIdle();

  const reads = await gw.watchGridReads();
  await gw.cloneTileCell(cx, cy, cx - 2, cy);
  const copy = tileAt(await gw.getGrid(f.gridID), 'text', cx - 2, cy)!;
  expect(copy, 'the clone landed').toBeTruthy();
  await expect
    .poll(async () => (await gw.focused()).tileIds.includes(copy.id), {
      message: 'the event put the copy in the pane',
      timeout: 10_000,
    })
    .toBe(true);
  await gw.waitIdle();
  await reads.stop();
  expect(reads.of(f.gridID), 'the clone read its grid back').toBe(0);
});

// The flip is written as the pane leaves the text: here by closing it, since
// an ascent reads the grid it lands on anyway and would hide a read behind its
// own. The other pane shows the same grid, so a read back would be asked.
test('a text view flip reads no listing back', async ({ gw, window }) => {
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const made = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(right.gridID, 'both panes show the grid').toBe(f.gridID);
  await gw.clickScreen(left.x + 20, left.y + 20);
  const lx = Math.round(left.cx);
  const ly = Math.round(left.cy) - 1;
  expect(tileAt(await gw.getGrid(f.gridID), 'text', lx, ly)?.id, 'the left pane frames the tile').toBe(made.id);
  await gw.descendCell(lx, ly);
  await gw.typeText('# flip');
  await settle(window, c.textSaveMs);

  const reads = await gw.watchGridReads();
  await gw.toggleTextMode();
  await gw.collapseLeftPane();
  await expect
    .poll(async () => (await gw.getGrid(f.gridID)).tiles?.find((t) => t.id === made.id)?.textMode ?? '', {
      message: 'the flip landed on the row',
      timeout: 10_000,
    })
    .toBe('rendered');
  await gw.waitIdle();
  await settle(window, c.framingSaveMs);
  await reads.stop();
  expect(reads.of(f.gridID), 'the flip read its grid back').toBe(0);
});
