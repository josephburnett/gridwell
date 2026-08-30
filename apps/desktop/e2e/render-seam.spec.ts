import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Closes the create, cache, render seam. Asserting only that a created tile
// lands on the server, through the getGrid oracle, leaves the render half
// unobserved. The panes() hook exposes each pane's rendered tile ids, its cache
// contents, so these specs assert both halves: the tile is on the server and it
// is drawn. A tile in the oracle but absent from the pane's tileIds is exactly
// "it just disappeared".

test('a text tile created in a descended grid is rendered, not just persisted', async ({ gw }) => {
  await gw.enterPlugin('home');
  let f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  // Create a well in the root grid and descend into it.
  await gw.openPalette();
  await gw.dragCreate('well', wx, wy);
  await gw.descendCell(wx, wy);
  f = await gw.focused();
  const childGrid = f.gridID;
  expect(childGrid, 'descended into the child grid').not.toBe(rootGrid);

  // Drop a text tile, whose palette kind is "markdown" and store kind "text",
  // into the descended grid.
  const tx = Math.round(f.cx);
  const ty = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', tx, ty);

  // Server truth.
  const onServer = tileAt(await gw.getGrid(childGrid), 'text', tx, ty);
  expect(onServer, 'text tile persisted on the server').toBeTruthy();

  // Render truth: the focused pane draws it. Polled, because the create's
  // optimistic commit and the background fetchGrid land on their own schedule;
  // the bug guarded here is a tile that never appears, not one that appears a
  // beat later.
  await expect
    .poll(async () => (await gw.focused()).tileIds, {
      message: 'the created text tile is rendered by the pane, not silently dropped',
      timeout: 10_000,
    })
    .toContain(onServer!.id);
});

// Clone is an eager, independent copy, so after the gesture both tiles must
// exist on the server and both must be rendered. Picking one up must not make
// the other vanish until it is put down.
test('cloning a tile leaves both the original and the copy rendered', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  // Right-drag from the tile's center to the adjacent cell: the clone gesture.
  await gw.cloneTileCell(cx, cy, cx + 1, cy);

  // Server truth: two independent wells.
  const snap = await gw.getGrid(grid);
  const orig = tileAt(snap, 'well', cx, cy);
  const copy = tileAt(snap, 'well', cx + 1, cy);
  expect(orig, 'original well still on the server').toBeTruthy();
  expect(copy, 'clone created on the server').toBeTruthy();
  expect(orig!.id, 'clone is a distinct tile (no id reassignment)').not.toBe(copy!.id);

  // Render truth: neither the original nor the clone disappeared from the pane.
  // Polled like the create spec above, since the optimistic commit and the
  // background refetch land on their own schedule; the guarded bug is a tile
  // that never comes back.
  await expect
    .poll(async () => (await gw.focused()).tileIds, {
      message: 'the original is still rendered after the clone',
      timeout: 10_000,
    })
    .toContain(orig!.id);
  await expect
    .poll(async () => (await gw.focused()).tileIds, {
      message: 'the clone is rendered',
      timeout: 10_000,
    })
    .toContain(copy!.id);
});

// Moving a tile must not lose it from the render: after a move it is still
// drawn, now at the destination cell.
test('a moved tile stays rendered at its destination', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const before = tileAt(await gw.getGrid(grid), 'well', cx, cy)!;

  await gw.dragTileCell(cx, cy, cx + 1, cy);

  // Server: same tile id, new cell. A move is in place; the id never changes.
  const moved = tileAt(await gw.getGrid(grid), 'well', cx + 1, cy);
  expect(moved, 'tile is at the destination cell on the server').toBeTruthy();
  expect(moved!.id, 'a move keeps the same tile id').toBe(before.id);

  // Render: the tile is still drawn and did not vanish during the move.
  expect((await gw.focused()).tileIds, 'the moved tile is still rendered').toContain(before.id);
});

// Deleting a tile must remove it from the render too: the delete reflects rather
// than leaving a ghost the cache still draws.
test('a deleted tile is removed from the render', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const created = tileAt(await gw.getGrid(grid), 'well', cx, cy)!;
  expect((await gw.focused()).tileIds, 'created tile is rendered').toContain(created.id);

  await gw.deleteTileCell(cx, cy);

  // Gone from the server and from the render. The render removal arrives through
  // the TileRemoved fan-out into the cache and a redraw, so poll it rather than
  // reading once.
  expect(tileAt(await gw.getGrid(grid), 'well', cx, cy), 'tile removed on the server').toBeFalsy();
  await expect
    .poll(async () => (await gw.focused()).tileIds.includes(created.id), { timeout: 5_000 })
    .toBe(false);
});
