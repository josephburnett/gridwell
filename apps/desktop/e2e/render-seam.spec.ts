import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Closes the create→cache→render seam that create-in-descent.spec.ts could only
// half-cover: it asserts a created tile lands on the SERVER (the getGrid oracle),
// then notes the remaining gap in a comment — "any 'disappear' is a render/cache
// bug (follow up against panes())". There was no follow-up because what a pane
// actually renders wasn't observable. The panes() hook now exposes each pane's
// rendered tile ids (its cache contents), so these specs assert both halves: the
// tile is on the server AND it is drawn. A tile in the oracle but absent from the
// pane's tileIds is exactly the owner's "it just disappeared".

test('a text tile created in a descended grid is rendered, not just persisted', async ({ gw }) => {
  await gw.enterPlugin('localdb');
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

  // Drop a text ("markdown" → store kind "text") tile — the kind the owner saw
  // vanish — into the descended grid.
  const tx = Math.round(f.cx);
  const ty = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', tx, ty);

  // Server truth.
  const onServer = tileAt(await gw.getGrid(childGrid), 'text', tx, ty);
  expect(onServer, 'text tile persisted on the server').toBeTruthy();

  // Render truth: the focused pane actually draws it (the gap create-in-descent
  // left open).
  const after = await gw.focused();
  expect(
    after.tileIds,
    'the created text tile is rendered by the pane, not silently dropped',
  ).toContain(onServer!.id);
});

// Regression guard for a specific owner report: "when I've cloned a tile … I
// pick up one, the other disappears until I put it down." Clone is an eager,
// independent copy (CLAUDE.md), so after the gesture BOTH tiles must exist on the
// server AND both must be rendered — neither vanishes.
test('cloning a tile leaves both the original and the copy rendered', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  // Right-drag from the tile's center to the adjacent cell → the clone gesture.
  await gw.cloneTileCell(cx, cy, cx + 1, cy);

  // Server truth: two independent wells.
  const snap = await gw.getGrid(grid);
  const orig = tileAt(snap, 'well', cx, cy);
  const copy = tileAt(snap, 'well', cx + 1, cy);
  expect(orig, 'original well still on the server').toBeTruthy();
  expect(copy, 'clone created on the server').toBeTruthy();
  expect(orig!.id, 'clone is a distinct tile (no id reassignment)').not.toBe(copy!.id);

  // Render truth: neither the original nor the clone disappeared from the pane.
  const rendered = (await gw.focused()).tileIds;
  expect(rendered, 'the original is still rendered after the clone').toContain(orig!.id);
  expect(rendered, 'the clone is rendered').toContain(copy!.id);
});

// Moving a tile must not lose it from the render (the owner's "I pick it up and
// it disappears"): after a move it is still drawn, now at the destination cell.
test('a moved tile stays rendered at its destination', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const before = tileAt(await gw.getGrid(grid), 'well', cx, cy)!;

  await gw.dragTileCell(cx, cy, cx + 1, cy);

  // Server: same tile id, new cell (a move is in-place — the id never changes).
  const moved = tileAt(await gw.getGrid(grid), 'well', cx + 1, cy);
  expect(moved, 'tile is at the destination cell on the server').toBeTruthy();
  expect(moved!.id, 'a move keeps the same tile id').toBe(before.id);

  // Render: the tile is still drawn (it didn't vanish during the move).
  expect((await gw.focused()).tileIds, 'the moved tile is still rendered').toContain(before.id);
});

// Deleting a tile must remove it from the render too — the delete reflects, it
// doesn't leave a ghost the cache still draws.
test('a deleted tile is removed from the render', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const created = tileAt(await gw.getGrid(grid), 'well', cx, cy)!;
  expect((await gw.focused()).tileIds, 'created tile is rendered').toContain(created.id);

  await gw.deleteTileCell(cx, cy);

  // Gone from the server AND from the render. The render removal arrives via the
  // TileRemoved fan-out → cache → redraw, so poll it rather than reading once.
  expect(tileAt(await gw.getGrid(grid), 'well', cx, cy), 'tile removed on the server').toBeFalsy();
  await expect
    .poll(async () => (await gw.focused()).tileIds.includes(created.id), { timeout: 5_000 })
    .toBe(false);
});
