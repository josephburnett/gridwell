import { test, expect } from './fixtures';
import { tileAt, GridSnapshot } from './oracle';

// The tile-manipulation gestures over the real canvas, each asserted against
// the server oracle, which is the ground truth for what mutated. Offsets stay
// small and aim inward from the viewport center, because a drop must land on
// the canvas: the wasm mouseup listener is bound to the canvas element.

function countKind(snap: GridSnapshot, kind: string): number {
  return (snap.tiles ?? []).filter((t) => t.kind === kind).length;
}

test('tile gestures (move, copy, resize, delete) mutate server state', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  // From the center cell toward the upper-left interior, clear of the edges.
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // ── MOVE ────────────────────────────────────────────────────────────────
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  expect(tileAt(await gw.getGrid(grid), 'text', cx, cy), 'created text tile').toBeTruthy();

  await gw.dragTileCell(cx, cy, cx - 2, cy);
  let snap = await gw.getGrid(grid);
  expect(tileAt(snap, 'text', cx - 2, cy), 'tile moved to the new cell').toBeTruthy();
  expect(tileAt(snap, 'text', cx, cy), 'old cell vacated').toBeFalsy();

  // ── COPY ───────────────────────────────────────────────────────────────
  const before = countKind(await gw.getGrid(grid), 'text');
  await gw.copyTileCell(cx - 2, cy, cx - 2, cy - 2);
  snap = await gw.getGrid(grid);
  expect(countKind(snap, 'text'), 'copy added one text tile').toBe(before + 1);
  expect(tileAt(snap, 'text', cx - 2, cy - 2), 'copy at the drop cell').toBeTruthy();
  expect(tileAt(snap, 'text', cx - 2, cy), 'original still in place (eager copy)').toBeTruthy();

  // ── RESIZE ──────────────────────────────────────────────────────────────
  const orig = tileAt(await gw.getGrid(grid), 'text', cx - 2, cy - 2)!;
  expect(Number(orig.w), 'starts 1 wide').toBe(1);
  await gw.resizeTileCell(cx - 2, cy - 2, cx, cy);
  const resized = tileAt(await gw.getGrid(grid), 'text', cx - 2, cy - 2)!;
  expect(Number(resized.w), 'tile grew wider').toBeGreaterThan(1);
  expect(Number(resized.h), 'tile grew taller').toBeGreaterThan(1);

  // ── DELETE ──────────────────────────────────────────────────────────────
  // The resized copy now spans where the moved original sits, so trash a fresh
  // 1x1 tile instead and keep the target unambiguous.
  await gw.openMenu();
  await gw.dragCreate('markdown', cx + 1, cy + 1);
  const beforeDel = countKind(await gw.getGrid(grid), 'text');
  await gw.deleteTileCell(cx + 1, cy + 1);
  snap = await gw.getGrid(grid);
  expect(countKind(snap, 'text'), 'delete removed one text tile').toBe(beforeDel - 1);
  expect(tileAt(snap, 'text', cx + 1, cy + 1), 'deleted tile is gone').toBeFalsy();
});

// A multi-cell tile whose new footprint overlaps its old one must still move:
// the client's drop preflight excludes the moving tile, as PlaceTile does, or
// the drag snaps back with nothing visibly in the way.
test('a multi-cell tile moves one cell into its own old footprint (#231)', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx) - 1;
  const cy = Math.round(f.cy) - 1;

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await gw.resizeTileCell(cx, cy, cx + 2, cy + 2);
  const t = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(Number(t.w), '2x2 footprint').toBe(2);

  // One cell right: the new rect [cx+1..cx+2] overlaps the old [cx..cx+1].
  await gw.dragTileCell(cx, cy, cx + 1, cy);
  const snap = await gw.getGrid(grid);
  const moved = (snap.tiles ?? []).find((n: any) => n.id === t.id)!;
  expect(
    { x: Number(moved.x ?? 0), y: Number(moved.y ?? 0) },
    'the tile moved one cell despite crossing itself',
  ).toEqual({ x: cx + 1, y: cy });
});
