import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { placeTile, setSelection, tileAt, Tile } from './oracle';
import type { GridwellDriver } from './driver';

// A gesture on a selected tile acts on its grid's whole selection
// (dragdrop.CarryOf): a left-drag moves the set, a right-drag clones it, ctrl
// + right-drag links it, the trashcan deletes it, each tile its own write and
// claim. The selection is made through the node (SetSelection, as another
// window would), not a gesture, so these pin what the set does and nothing
// about how it is made. The server's rows are the oracle.

const WRITES = ['PlaceTile', 'CloneTile', 'CreateTile', 'SetTile', 'DeleteTile', 'SetFraming', 'WriteContent'];

async function watchWrites(win: Page): Promise<() => string[]> {
  const seen: string[] = [];
  await win.route('**/gridwell.v1.Gridwell/*', async (r) => {
    const m = r.request().url().split('/').pop() ?? '';
    if (WRITES.includes(m)) seen.push(m);
    await r.continue();
  });
  return () => seen.slice();
}

function ghost(win: Page): Promise<{ active: boolean; hidden: string[]; carried: string[] }> {
  return win.evaluate(() => (window as any).__gridwellTest.ghost());
}

// Two selected text tiles two cells apart on one row, and an unselected one
// below them, all held by the client with the selection.
async function selectedPair(gw: GridwellDriver) {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const at = [[cx - 1, cy], [cx + 1, cy], [cx - 1, cy + 3]];
  const tiles: Tile[] = [];
  for (const [x, y] of at) {
    await gw.openPalette();
    await gw.dragCreate('markdown', x, y);
    const t = tileAt(await gw.getGrid(f.gridID), 'text', x, y)!;
    expect(t, `created a text tile at ${x},${y}`).toBeTruthy();
    tiles.push(t);
  }
  const [a, b, c] = tiles;
  await setSelection(gw.origin, f.gridID, [a.id, b.id]);
  await expect.poll(async () => (await gw.focused()).selection.slice().sort()).toEqual([a.id, b.id].sort());
  await gw.waitIdle();
  return { f, cx, cy, a, b, c };
}

function texts(snap: { tiles?: Tile[] }): Tile[] {
  return (snap.tiles ?? []).filter((t) => t.kind === 'text');
}

test('a left-drag on a selected tile moves the set; a stale tile is refused and stays alone', async ({ gw, window }) => {
  const { f, cx, cy, a, b, c } = await selectedPair(gw);
  const m = window.mouse;
  const from = await gw.cellCenter(f.id, cx - 1, cy);
  const to = await gw.cellCenter(f.id, cx - 1, cy + 1);
  await m.move(from.x, from.y);
  await m.down({ button: 'left' });
  await m.move(from.x + 8, from.y + 8);
  const g = await ghost(window);
  expect(g.carried.slice().sort(), 'the ghost carries the set').toEqual([a.id, b.id].sort());
  expect(g.hidden.slice().sort(), 'the move hides the set').toEqual([a.id, b.id].sort());
  // Another window moves b before the drop, so the press's claim on b is stale.
  await placeTile(gw.origin, b.id, f.gridID, cx + 4, cy - 2, 1, 1);
  await m.move(to.x, to.y, { steps: 8 });
  await m.up({ button: 'left' });
  await gw.waitIdle();

  await gw.waitClientTileAt(f.id, cx - 1, cy + 1, a.id);
  await gw.waitClientTileAt(f.id, cx + 4, cy - 2, b.id);
  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx - 1, cy + 1)?.id, 'a moved by the offset').toBe(a.id);
  expect(tileAt(snap, 'text', cx + 4, cy - 2)?.id, "b stays where the other window put it").toBe(b.id);
  expect(tileAt(snap, 'text', cx + 1, cy + 1), 'nothing landed in b\'s cell').toBeFalsy();
  expect(tileAt(snap, 'text', cx - 1, cy + 3)?.id, 'the unselected tile did not move').toBe(c.id);
});

test('a set move keeps its layout even when a tile lands where another just left', async ({ gw }) => {
  const { cx, cy, a, b, c } = await selectedPair(gw);
  const f = await gw.focused();
  // One step right by two: a lands in b's old cell, which b vacates.
  await gw.dragTileCell(cx - 1, cy, cx + 1, cy);
  await gw.waitClientTileAt(f.id, cx + 3, cy, b.id);
  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx + 1, cy)?.id, 'a landed in the cell b vacated').toBe(a.id);
  expect(tileAt(snap, 'text', cx + 3, cy)?.id, 'b moved by the same offset').toBe(b.id);
  expect(tileAt(snap, 'text', cx - 1, cy + 3)?.id).toBe(c.id);
});

test('a drag on an unselected tile moves it alone and leaves the selection be', async ({ gw }) => {
  const { f, cx, cy, a, b, c } = await selectedPair(gw);
  await gw.dragTileCell(cx - 1, cy + 3, cx - 1, cy + 5);
  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx - 1, cy + 5)?.id).toBe(c.id);
  expect(tileAt(snap, 'text', cx - 1, cy)?.id, 'a stayed').toBe(a.id);
  expect(tileAt(snap, 'text', cx + 1, cy)?.id, 'b stayed').toBe(b.id);
  expect((await gw.focused()).selection.slice().sort()).toEqual([a.id, b.id].sort());
});

test('a set move onto a cell the set does not vacate is refused whole', async ({ gw, window }) => {
  const { f, cx, cy, a, b, c } = await selectedPair(gw);
  const writes = await watchWrites(window);
  // b would land on c.
  await gw.dragTileCell(cx - 1, cy, cx - 3, cy + 3);
  expect(writes(), 'nothing was sent').toEqual([]);
  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx - 1, cy)?.id).toBe(a.id);
  expect(tileAt(snap, 'text', cx + 1, cy)?.id).toBe(b.id);
  expect(tileAt(snap, 'text', cx - 1, cy + 3)?.id).toBe(c.id);
});

test('a right-drag on a selected tile clones the set in its layout', async ({ gw }) => {
  const { f, cx, cy, a, b } = await selectedPair(gw);
  const n = texts(await gw.getGrid(f.gridID)).length;
  await gw.cloneTileCell(cx - 1, cy, cx - 1, cy - 2);
  const snap = await gw.getGrid(f.gridID);
  expect(texts(snap).length, 'two copies').toBe(n + 2);
  const ca = tileAt(snap, 'text', cx - 1, cy - 2);
  const cb = tileAt(snap, 'text', cx + 1, cy - 2);
  for (const [copy, src] of [[ca, a], [cb, b]] as const) {
    expect(copy, 'a copy at the offset').toBeTruthy();
    expect(copy!.id).not.toBe(src.id);
    expect(copy!.reference ?? false, 'a copy, not a link').toBe(false);
  }
  expect(tileAt(snap, 'text', cx - 1, cy)?.id, 'the sources stay').toBe(a.id);
  expect(tileAt(snap, 'text', cx + 1, cy)?.id).toBe(b.id);
});

test('a ctrl + right-drag on a selected tile links the set', async ({ gw }) => {
  const { f, cx, cy, a, b } = await selectedPair(gw);
  await gw.linkTileCell(cx - 1, cy, cx - 1, cy - 2);
  const snap = await gw.getGrid(f.gridID);
  const la = tileAt(snap, 'text', cx - 1, cy - 2);
  const lb = tileAt(snap, 'text', cx + 1, cy - 2);
  expect(la?.linkTargetId, 'a link to a').toBe(a.id);
  expect(lb?.linkTargetId, 'a link to b').toBe(b.id);
  expect(la?.reference).toBe(true);
  expect(lb?.reference).toBe(true);
});

test('dropping a selected tile on the trashcan deletes the set', async ({ gw }) => {
  const { f, cx, cy, c } = await selectedPair(gw);
  await gw.deleteTileCell(cx - 1, cy);
  await expect.poll(async () => texts(await gw.getGrid(f.gridID)).map((t) => t.id)).toEqual([c.id]);
});

test('Esc mid set-drag returns every tile and writes nothing', async ({ gw, window }) => {
  const { f, cx, cy, a, b } = await selectedPair(gw);
  const writes = await watchWrites(window);
  const m = window.mouse;
  const from = await gw.cellCenter(f.id, cx - 1, cy);
  const to = await gw.cellCenter(f.id, cx - 1, cy - 2);
  await m.move(from.x, from.y);
  await m.down({ button: 'left' });
  await m.move(from.x + 8, from.y + 8);
  await m.move(to.x, to.y, { steps: 8 });
  expect((await ghost(window)).carried.length, 'the set is in flight').toBe(2);
  await window.keyboard.press('Escape');
  await m.move(to.x + 3, to.y + 3);
  await m.up({ button: 'left' });
  await gw.waitIdle();
  await expect.poll(async () => (await ghost(window)).active, 'the ghost went home').toBe(false);
  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx - 1, cy)?.id).toBe(a.id);
  expect(tileAt(snap, 'text', cx + 1, cy)?.id).toBe(b.id);
  expect(writes(), 'no write left the client').toEqual([]);
});
