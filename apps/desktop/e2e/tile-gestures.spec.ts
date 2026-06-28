import { test, expect } from './fixtures';
import { tileAt, GridSnapshot } from './oracle';

// Drives the tile-manipulation gestures over the real canvas and asserts each
// against the server oracle (GetGrid) — the gestures are opaque on the canvas,
// so the server's record is the ground truth for what actually mutated.
//
// All palette primitives are 1x1, so a tile occupies exactly one cell. Cells
// render large (~150px), so offsets are kept small and AIMED INWARD from the
// viewport center: a drop must land on the canvas, since the wasm mouseup
// listener is bound to the canvas element — releasing off-canvas would strand
// the drag (and is not a real user action on a maximized window).

function countKind(snap: GridSnapshot, kind: string): number {
  return (snap.tiles ?? []).filter((t) => t.kind === kind).length;
}

test('tile gestures (move, clone, resize, delete) mutate server state', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const grid = f.gridID;
  // Center cell, then work toward the upper-left interior (room from the edges).
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // ── MOVE ────────────────────────────────────────────────────────────────
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  expect(tileAt(await gw.getGrid(grid), 'text', cx, cy), 'created text tile').toBeTruthy();

  await gw.dragTileCell(cx, cy, cx - 2, cy);
  let snap = await gw.getGrid(grid);
  expect(tileAt(snap, 'text', cx - 2, cy), 'tile moved to the new cell').toBeTruthy();
  expect(tileAt(snap, 'text', cx, cy), 'old cell vacated').toBeFalsy();

  // ── CLONE ───────────────────────────────────────────────────────────────
  const before = countKind(await gw.getGrid(grid), 'text');
  await gw.cloneTileCell(cx - 2, cy, cx - 2, cy - 2);
  snap = await gw.getGrid(grid);
  expect(countKind(snap, 'text'), 'clone added one text tile').toBe(before + 1);
  expect(tileAt(snap, 'text', cx - 2, cy - 2), 'clone at the drop cell').toBeTruthy();
  expect(tileAt(snap, 'text', cx - 2, cy), 'original still in place (eager copy)').toBeTruthy();

  // ── RESIZE ──────────────────────────────────────────────────────────────
  const orig = tileAt(await gw.getGrid(grid), 'text', cx - 2, cy - 2)!;
  expect(Number(orig.w), 'starts 1 wide').toBe(1);
  await gw.resizeTileCell(cx - 2, cy - 2, cx, cy);
  const resized = tileAt(await gw.getGrid(grid), 'text', cx - 2, cy - 2)!;
  expect(Number(resized.w), 'tile grew wider').toBeGreaterThan(1);
  expect(Number(resized.h), 'tile grew taller').toBeGreaterThan(1);

  // ── DELETE ──────────────────────────────────────────────────────────────
  // Delete the moved original (the 1x1 at (cx-2, cy)); the resized clone now
  // spans that area, so delete a fresh tile instead to keep the target a clean
  // 1x1. Create one, then trash it.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 1, cy + 1);
  const beforeDel = countKind(await gw.getGrid(grid), 'text');
  await gw.deleteTileCell(cx + 1, cy + 1);
  snap = await gw.getGrid(grid);
  expect(countKind(snap, 'text'), 'delete removed one text tile').toBe(beforeDel - 1);
  expect(tileAt(snap, 'text', cx + 1, cy + 1), 'deleted tile is gone').toBeFalsy();
});
