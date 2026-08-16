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
  await gw.enterPlugin('local');
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

// #231: a multi-cell tile dragged a SHORT distance — its new footprint
// overlapping its own old one — must move. The client's drop preflight
// used to count the moving tile itself as an obstacle (the server's
// PlaceTile has excluded self all along) and snapped the drag back with
// nothing visibly in the way; dragging far away and back again "worked"
// because the old footprint was no longer under the target.
test('a multi-cell tile moves one cell into its own old footprint (#231)', async ({ gw }) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx) - 1;
  const cy = Math.round(f.cy) - 1;

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.resizeTileCell(cx, cy, cx + 2, cy + 2);
  const t = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(Number(t.w), '2x2 footprint').toBe(2);

  // One cell right: new rect [cx+1..cx+2] overlaps old [cx..cx+1].
  await gw.dragTileCell(cx, cy, cx + 1, cy);
  const snap = await gw.getGrid(grid);
  const moved = (snap.tiles ?? []).find((n: any) => n.id === t.id)!;
  expect(
    { x: Number(moved.x ?? 0), y: Number(moved.y ?? 0) },
    'the tile moved one cell despite crossing itself',
  ).toEqual({ x: cx + 1, y: cy });
});
