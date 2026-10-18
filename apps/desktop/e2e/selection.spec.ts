import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { createLeafLink, moveTile, setSelection, tileAt, Tile } from './oracle';
import { pickFromNativeMenu } from './choicemenu';
import type { GridwellDriver } from './driver';

// The selection is a node fact on the grid row (2026-10-15). These cross from
// the gesture to the row the node holds and back to the border every view
// draws: shift + left-click toggles, shift + left-drag sweeps a band and takes
// only the tiles wholly inside it, Esc with nothing in flight clears, the +
// menu's Clear selections empties every set, and inside a pane tile the set is
// the grid row's too, shown in every pane on the grid (2026-10-18).

async function textTile(gw: GridwellDriver, cx: number, cy: number): Promise<Tile> {
  const f = await gw.focused();
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(t, `text tile at ${cx},${cy}`).toBeTruthy();
  await gw.waitClientTileAt(f.id, cx, cy, t.id);
  return t;
}

async function onNode(gw: GridwellDriver, gridID: string): Promise<string[]> {
  return ((await gw.getGrid(gridID)).grid?.selection ?? []).slice().sort();
}

async function painted(win: Page, paneID: string): Promise<string[]> {
  const all = await win.evaluate(() => (window as any).__gridwellTest.selectionPainted());
  return ((all[paneID] ?? []) as string[]).slice().sort();
}

async function expectSelection(gw: GridwellDriver, win: Page, paneID: string, gridID: string, want: string[], why: string) {
  const sorted = want.slice().sort();
  await expect.poll(() => onNode(gw, gridID), { message: `${why}: the grid row`, timeout: 10_000 }).toEqual(sorted);
  await expect.poll(() => painted(win, paneID), { message: `${why}: the yellow border` }).toEqual(sorted);
}

async function shiftClick(gw: GridwellDriver, win: Page, cx: number, cy: number) {
  const f = await gw.focused();
  const c = await gw.cellCenter(f.id, cx, cy);
  await win.keyboard.down('Shift');
  await win.mouse.click(c.x, c.y);
  await win.keyboard.up('Shift');
  await gw.waitIdle();
}

async function shiftDrag(gw: GridwellDriver, win: Page, from: [number, number], to: [number, number]) {
  const f = await gw.focused();
  const a = await gw.cellCenter(f.id, from[0], from[1]);
  const b = await gw.cellCenter(f.id, to[0], to[1]);
  await win.keyboard.down('Shift');
  await win.mouse.move(a.x, a.y);
  await win.mouse.down();
  await win.mouse.move(a.x + 8, a.y + 8);
  await win.mouse.move(b.x, b.y, { steps: 8 });
  await win.mouse.up();
  await win.keyboard.up('Shift');
  await gw.waitIdle();
}

// The pixel just inside the tile's left edge, halfway down, is the theme's
// selection yellow: the border is drawn, not only recorded.
async function edgePixel(gw: GridwellDriver, win: Page, cx: number, cy: number): Promise<number[]> {
  const f = await gw.focused();
  const c = await gw.cellCenter(f.id, cx, cy);
  const n = await gw.cellCenter(f.id, cx + 1, cy);
  const x = c.x - (n.x - c.x) / 2 + 1.5;
  return win.evaluate(
    ([px, py]) => {
      const canvas = document.querySelector('canvas')!;
      const r = canvas.getBoundingClientRect();
      const sx = canvas.width / r.width;
      const d = canvas.getContext('2d')!.getImageData(Math.round(px * sx), Math.round(py * sx), 1, 1).data;
      return [d[0], d[1], d[2]];
    },
    [x, c.y],
  );
}

test('shift-click toggles a tile, the border is yellow, a plain click leaves the set, and it survives a reload', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const a = await textTile(gw, cx, cy);
  const b = await textTile(gw, cx + 2, cy);

  await shiftClick(gw, window, cx, cy);
  await expectSelection(gw, window, f.id, f.gridID, [a.id], 'shift-click A');
  const yellow = await edgePixel(gw, window, cx, cy);
  expect(yellow, 'the border wears the theme yellow #f7ea1c').toEqual([0xf7, 0xea, 0x1c]);
  expect(await edgePixel(gw, window, cx + 2, cy), 'an unselected tile has no yellow').not.toEqual(yellow);
  expect((await gw.focused()).placeDepth, 'a shift-click does not descend').toBe(0);

  // A plain click on the background is no selection gesture.
  const bg = await gw.cellCenter(f.id, cx, cy + 2);
  await window.mouse.click(bg.x, bg.y);
  await gw.waitIdle();
  await expectSelection(gw, window, f.id, f.gridID, [a.id], 'a plain background click');

  await shiftClick(gw, window, cx + 2, cy);
  await expectSelection(gw, window, f.id, f.gridID, [a.id, b.id], 'shift-click B');
  await shiftClick(gw, window, cx, cy);
  await expectSelection(gw, window, f.id, f.gridID, [b.id], 'shift-click A again');

  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await window.waitForFunction(
    () => {
      try {
        return ((window as any).__gridwellTest.panes() as Array<{ focused: boolean }>).some((p) => p.focused);
      } catch {
        return false;
      }
    },
    null,
    { timeout: 30_000 },
  );
  await gw.waitIdle();
  const g = await gw.focused();
  expect(g.gridID, 'the reload came back to the grid').toBe(f.gridID);
  await expectSelection(gw, window, g.id, g.gridID, [b.id], 'after a reload');
});

test('a shift-drag band takes the tiles wholly inside it, never one it only touches; a plain drag pans', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const inside = await textTile(gw, cx, cy);
  await textTile(gw, cx + 2, cy);
  await gw.resizeTileCell(cx + 2, cy, cx + 4, cy + 1);
  const wide = tileAt(await gw.getGrid(f.gridID), 'text', cx + 2, cy)!;
  expect([Number(wide.w), Number(wide.h)], 'the second tile spans two cells').toEqual([2, 1]);

  // Cells cx-1..cx+2 by cy-1..cy+1: the first tile inside, the wide one half in.
  await shiftDrag(gw, window, [cx - 1, cy - 1], [cx + 2, cy + 1]);
  await expectSelection(gw, window, f.id, f.gridID, [inside.id], 'the band');
  expect((await window.evaluate(() => (window as any).__gridwellTest.idleDetail())).band, 'the band let go').toBe(
    false,
  );
  expect((await gw.focused()).cx, 'a shift-drag does not pan').toBeCloseTo(f.cx, 5);

  await gw.panFocusedGrid(cx - 1, cy + 2, cx - 2, cy + 2);
  expect((await gw.focused()).cx, 'a plain background drag pans').not.toBeCloseTo(f.cx, 1);
  await expectSelection(gw, window, f.id, f.gridID, [inside.id], 'after a pan');
});

test('Esc with no drag in flight clears the selection, and with none selected writes nothing', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const a = await textTile(gw, cx, cy);
  await shiftClick(gw, window, cx, cy);
  await expectSelection(gw, window, f.id, f.gridID, [a.id], 'selected');

  await window.keyboard.press('Escape');
  await gw.waitIdle();
  await expectSelection(gw, window, f.id, f.gridID, [], 'Esc');

  const sent: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/SetSelection', async (r) => {
    sent.push('SetSelection');
    await r.continue();
  });
  await window.keyboard.press('Escape');
  await gw.waitIdle();
  expect(sent, 'Esc on an empty selection wrote nothing').toEqual([]);
});

// descendPaneTile makes a pane tile at the focused pane's center, beside a
// text tile, and goes in: the pane inside shows the same grid.
async function descendPaneTile(gw: GridwellDriver, win: Page) {
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(f.gridID), 'pane', wx, wy)!;
  expect(pt, 'pane tile').toBeTruthy();
  const a = await textTile(gw, wx + 2, wy);
  await gw.descendCell(wx, wy);
  await expect
    .poll(async () => (await win.evaluate(() => (window as any).__gridwellTest.workspace())).depth)
    .toBe(1);
  const inner = await gw.focused();
  expect(inner.gridID, 'the pane inside shows the same grid').toBe(f.gridID);
  await gw.waitClientTileAt(inner.id, wx + 2, wy, a.id);
  return { f, pt, a, inner, wx, wy };
}

test('inside a pane tile the selection is the grid row\'s, every pane on the grid shows it, and the layout holds none', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const { f, pt, a, inner, wx, wy } = await descendPaneTile(gw, window);

  await shiftClick(gw, window, wx + 2, wy);
  await expectSelection(gw, window, inner.id, f.gridID, [a.id], 'shift-click inside');
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes, 'two panes inside').toHaveLength(2);
  for (const p of panes) {
    expect(p.gridID, `pane ${p.id} shows the grid`).toBe(f.gridID);
    await expect.poll(() => painted(window, p.id), { message: `pane ${p.id} shows the row's set` }).toEqual([a.id]);
  }
  await gw.waitIdle();
  expect(await gw.getTileContent(pt.id), 'the layout blob holds no set').not.toContain('"sel"');

  await gw.leaveWorkspace();
  await expect
    .poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth)
    .toBe(0);
  const out = await gw.focused();
  await expect.poll(() => painted(window, out.id), { message: 'outside, the same set' }).toEqual([a.id]);

  await gw.descendCell(wx, wy);
  await expect
    .poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth)
    .toBe(1);
  const back = await gw.focused();
  await window.keyboard.press('Escape');
  await gw.waitIdle();
  await expectSelection(gw, window, back.id, f.gridID, [], 'Esc inside clears the row');
});

test('inside a pane tile a tile that leaves the grid leaves the set, and comes back unselected', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('well', wx, wy + 2);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', wx, wy + 2)!;
  expect(well?.childGridId, 'a well with a grid of its own').toBeTruthy();
  const { a, inner: inside } = await descendPaneTile(gw, window);
  await shiftClick(gw, window, wx + 2, wy);
  await expectSelection(gw, window, inside.id, f.gridID, [a.id], 'selected inside');

  // Another window moves the tile into the well and back to its cell.
  await moveTile(gw.origin, a.id, well.childGridId!, 0, 0, Number(a.w ?? 1), Number(a.h ?? 1));
  await expect.poll(async () => (await gw.focused()).tileIds.includes(a.id), { message: 'a left' }).toBe(false);
  await moveTile(gw.origin, a.id, f.gridID, wx + 2, wy, Number(a.w ?? 1), Number(a.h ?? 1));
  await gw.waitClientTileAt(inside.id, wx + 2, wy, a.id);
  await gw.waitIdle();
  await expectSelection(gw, window, inside.id, f.gridID, [], 'back, unselected');
});

// Clear selections, the + circle's right-click row, empties every grid's set
// the node holds, not only the one on screen.
test('Clear selections empties every grid\'s set', async ({ electronApp, gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const a = await textTile(gw, cx, cy);
  await gw.openMenu();
  await gw.dragCreate('well', cx + 2, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx + 2, cy)!;
  expect(well?.childGridId, 'a well with a grid of its own').toBeTruthy();
  const b = await createLeafLink(gw.origin, well.childGridId!, '', 'b.md', 0, 0);

  await shiftClick(gw, window, cx, cy);
  await setSelection(gw.origin, well.childGridId!, [b.id]);
  await expectSelection(gw, window, f.id, f.gridID, [a.id], 'selected here');
  expect(await onNode(gw, well.childGridId!), 'selected in the well').toEqual([b.id]);

  const picked = pickFromNativeMenu(electronApp, 'Clear selections');
  await gw.rightClickCircle();
  await picked;
  await expectSelection(gw, window, f.id, f.gridID, [], 'cleared here');
  await expect.poll(() => onNode(gw, well.childGridId!), { message: 'cleared in the well' }).toEqual([]);
});

// Inside a pane tile the set is the grid row's, so the node's clear reaches
// it like any other.
test('Clear selections empties the set a pane inside a pane tile shows', async ({ electronApp, gw, window }) => {
  await gw.enterPlugin('home');
  const { f, a, inner, wx, wy } = await descendPaneTile(gw, window);
  await shiftClick(gw, window, wx + 2, wy);
  await expectSelection(gw, window, inner.id, f.gridID, [a.id], 'selected inside');

  const picked = pickFromNativeMenu(electronApp, 'Clear selections');
  await gw.rightClickCircle();
  await picked;
  await expectSelection(gw, window, inner.id, f.gridID, [], 'cleared inside');
});
