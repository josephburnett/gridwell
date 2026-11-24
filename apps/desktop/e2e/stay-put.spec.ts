import { test, expect, homePassword, loginToken } from './fixtures';
import { settle } from './cadence';
import { tileAt } from './oracle';
import { dumpViaAPI } from './trace';
import type { GridwellDriver, PaneInfo } from './driver';

// A pane boundary the user drags covers or uncovers the grid a pane shows and
// never slides it (Joe, 2026-11-22, pane.StayPut), and an edge split's new
// pane shows what its rect covers (Joe, 2026-11-24): a split by a right-drag
// from any edge, a divider drag on either axis or at a corner, and the crush
// that closes a pane. A window resize and the split below, a boundary nobody
// dragged, keep the centre in the middle. The
// oracle is the shim's own projection (cellCenter), so a cell is where it
// was to the float, and the node's store writes, so none of it frames a row.

// Within this of a pixel: the layout's rects are floats and nothing rounds.
const PX = 2;

async function framingWrites(gw: GridwellDriver, token: string): Promise<number> {
  const lines = await dumpViaAPI(gw.origin, token);
  return lines.filter((l) => l.src === 'store' && l.kind === 'write' && l.msg.startsWith('SetFraming')).length;
}

async function posts(window: any): Promise<number> {
  return window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetFraming ?? 0));
}

async function at(gw: GridwellDriver, paneID: string, cx: number, cy: number) {
  return gw.cellCenter(paneID, cx, cy);
}

async function expectAt(gw: GridwellDriver, paneID: string, cx: number, cy: number, want: { x: number; y: number }, why: string) {
  const got = await at(gw, paneID, cx, cy);
  expect(got.x, `${why}: x`).toBeCloseTo(want.x, PX);
  expect(got.y, `${why}: y`).toBeCloseTo(want.y, PX);
}

// The new pane takes the quarter of the pane on the side, so the cell at
// the centre stays inside the original.
const sides = [
  { side: 'right', from: (p: PaneInfo) => [p.x + p.w - 5, p.y + p.h / 2], to: (p: PaneInfo) => [p.x + p.w * 0.75, p.y + p.h / 2] },
  { side: 'left', from: (p: PaneInfo) => [p.x + 5, p.y + p.h / 2], to: (p: PaneInfo) => [p.x + p.w * 0.25, p.y + p.h / 2] },
  { side: 'bottom', from: (p: PaneInfo) => [p.x + p.w / 2, p.y + p.h - 5], to: (p: PaneInfo) => [p.x + p.w / 2, p.y + p.h * 0.75] },
  { side: 'top', from: (p: PaneInfo) => [p.x + p.w / 2, p.y + 5], to: (p: PaneInfo) => [p.x + p.w / 2, p.y + p.h * 0.25] },
] as const;

async function drawnAt(window: any, paneID: string, cx: number, cy: number): Promise<string> {
  return window.evaluate(([id, x, y]: [string, number, number]) => (window as any).__gridwellTest.drawnAt(id, x, y), [
    paneID,
    cx,
    cy,
  ] as [string, number, number]);
}

function paneHolding(ps: PaneInfo[], pt: { x: number; y: number }): PaneInfo {
  const p = ps.find((q) => pt.x >= q.x && pt.x < q.x + q.w && pt.y >= q.y && pt.y < q.y + q.h);
  if (!p) throw new Error(`no pane holds (${pt.x},${pt.y})`);
  return p;
}

// An edge split slices the pane (Joe, 2026-11-24): tiles across the pane,
// one cut by where the boundary lands, are each drawn where they were, in
// whichever pane now holds them, the cut one in both.
for (const s of sides) {
  test(`a split from the ${s.side} edge slices the pane: every tile stays where it was and nothing writes framing`, async ({
    gw,
    home,
    window,
  }) => {
    const token = await loginToken(gw.origin, homePassword(home));
    const c = await gw.cadences();
    await gw.enterSource('home');
    const p0 = await gw.focused();
    const cell = (await at(gw, p0.id, 1, 0)).x - (await at(gw, p0.id, 0, 0)).x;
    const cellAt = (x: number, y: number) => [
      Math.floor(p0.cx + (x - (p0.x + p0.w / 2)) / cell),
      Math.floor(p0.cy + (y - (p0.y + p0.h / 2)) / cell),
    ];
    const frac = (fx: number, fy: number) => cellAt(p0.x + p0.w * fx, p0.y + p0.h * fy);
    const spots = [frac(0.12, 0.5), frac(0.88, 0.5), frac(0.5, 0.12), frac(0.5, 0.88)];
    for (const [x, y] of spots) {
      await gw.openMenu();
      await gw.dragCreate('markdown', x, y);
    }
    // The cut tile spans three cells across the boundary, its middle cell under it.
    const [bx, by] = s.to(p0);
    const vertical = s.side === 'left' || s.side === 'right';
    const [mx, my] = vertical ? cellAt(bx, p0.y + p0.h * 0.3) : cellAt(p0.x + p0.w * 0.3, by);
    const [ux, uy] = vertical ? [mx - 1, my] : [mx, my - 1];
    await gw.openMenu();
    await gw.dragCreate('markdown', ux, uy);
    await gw.resizeTileCell(ux, uy, vertical ? ux + 3 : ux + 1, vertical ? uy + 1 : uy + 3);
    const cut = tileAt(await gw.getGrid(p0.gridID), 'text', ux, uy);
    expect(cut, 'the cut tile was made').toBeTruthy();
    expect(vertical ? Number(cut!.w) : Number(cut!.h), 'the cut tile spans three cells').toBe(3);
    const ends = vertical ? [[ux, uy], [ux + 2, uy]] : [[ux, uy], [ux, uy + 2]];
    await settle(window, c.framingSaveMs);

    const cells = [...spots, ...ends];
    const before = await Promise.all(cells.map(([x, y]) => at(gw, p0.id, x, y)));
    const ids = await Promise.all(cells.map(([x, y]) => drawnAt(window, p0.id, x, y)));
    for (const id of ids) expect(id, 'each cell shows a tile before the split').not.toBe('');
    const writes0 = await framingWrites(gw, token);
    const posts0 = await posts(window);

    const [fx, fy] = s.from(p0);
    await gw.rightDragScreen(fx, fy, bx, by);
    const ps = await gw.panes();
    expect(ps.length, 'the drag split the pane').toBe(2);
    const orig = ps.find((p) => p.id === p0.id)!;
    expect(orig.w * orig.h, 'the original shrank').toBeLessThan(p0.w * p0.h);
    const holders: string[] = [];
    for (let k = 0; k < cells.length; k++) {
      const q = paneHolding(ps, before[k]);
      holders.push(q.id);
      await expectAt(gw, q.id, cells[k][0], cells[k][1], before[k], `cell ${cells[k]} in pane ${q.id} stays where it was`);
      expect(await drawnAt(window, q.id, cells[k][0], cells[k][1]), `pane ${q.id} draws the tile at ${cells[k]}`).toBe(ids[k]);
    }
    expect(new Set(holders).size, 'both panes hold a tile').toBe(2);
    expect(holders[cells.length - 2], 'the cut tile is drawn in both panes').not.toBe(holders[cells.length - 1]);
    for (const p of ps) expect(p.zoom, `pane ${p.id}'s zoom is untouched`).toBe(p0.zoom);

    await settle(window, c.framingSaveMs);
    expect(await posts(window), 'a split posts no SetFraming').toBe(posts0);
    expect(await framingWrites(gw, token), 'the node wrote no framing').toBe(writes0);
  });
}

// A pane descended into a content tile showed the content, not a grid, so
// the split's new pane ascends and lands as any ascent does: on the grid
// view it was copied with, the tile in view.
test('a split of a pane inside a text gives the new pane the ascent\'s landing', async ({ gw, window }) => {
  await gw.enterSource('home');
  const p0 = await gw.focused();
  const cx = Math.round(p0.cx);
  const cy = Math.round(p0.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).placeDepth, { message: 'descended into the text' }).toBe(1);
  const inside = await gw.focused();
  await gw.rightDragScreen(inside.x + inside.w - 5, inside.y + inside.h / 2, inside.x + inside.w * 0.5, inside.y + inside.h / 2);
  const ps = await gw.panes();
  expect(ps.length, 'the drag split the pane').toBe(2);
  const made = ps.find((p) => p.id !== p0.id)!;
  expect(made.placeDepth, 'the new pane ascended to the grid').toBe(0);
  expect([made.cx, made.cy, made.zoom], 'it lands on the view it was copied with').toEqual([p0.cx, p0.cy, p0.zoom]);
  expect(await drawnAt(window, made.id, cx, cy), 'the text is in view').not.toBe('');
});

// A split drag cancelled by Esc, its preview, and a split refused as too
// small change no pane and no view.
test('a split drag cancelled or refused moves nothing', async ({ gw, window }) => {
  await gw.enterSource('home');
  const p0 = await gw.focused();
  const x = p0.x + p0.w - 5;
  const y = p0.y + p0.h / 2;
  await window.mouse.move(x, y);
  await window.mouse.down({ button: 'right' });
  await window.mouse.move(x - p0.w * 0.4, y, { steps: 10 });
  expect(await gw.panes(), 'the preview splits nothing').toHaveLength(1);
  await window.keyboard.press('Escape');
  await window.mouse.up({ button: 'right' });
  await gw.waitIdle();
  await gw.rightDragScreen(x, y, p0.x + p0.w - 20, y);
  const now = await gw.panes();
  expect(now, 'neither split landed').toHaveLength(1);
  expect([now[0].x, now[0].w, now[0].cx, now[0].cy, now[0].zoom], 'the pane is as it was').toEqual([p0.x, p0.w, p0.cx, p0.cy, p0.zoom]);
});

// Two side by side, or one above the other, each showing its own centre cell.
async function twoPanes(gw: GridwellDriver, dir: 'v' | 'h') {
  await gw.enterSource('home');
  if (dir === 'v') await gw.splitFocusedPaneVertical();
  else await gw.splitFocusedPaneHorizontal();
  const ps = (await gw.panes()).slice().sort((a, b) => (dir === 'v' ? a.x - b.x : a.y - b.y));
  expect(ps.length).toBe(2);
  return ps;
}

async function cellsOf(gw: GridwellDriver, ps: PaneInfo[]) {
  return Promise.all(
    ps.map(async (p) => {
      const cx = Math.round(p.cx);
      const cy = Math.round(p.cy);
      return { id: p.id, cx, cy, pt: await at(gw, p.id, cx, cy) };
    }),
  );
}

async function expectCellsKept(gw: GridwellDriver, cells: Awaited<ReturnType<typeof cellsOf>>, why: string) {
  for (const k of cells) {
    await expectAt(gw, k.id, k.cx, k.cy, k.pt, `${why}: pane ${k.id}`);
  }
}

for (const dir of ['v', 'h'] as const) {
  test(`a ${dir === 'v' ? 'vertical' : 'horizontal'} divider drag leaves both grids where they were and writes no framing`, async ({
    gw,
    home,
    window,
  }) => {
    const token = await loginToken(gw.origin, homePassword(home));
    const c = await gw.cadences();
    const ps = await twoPanes(gw, dir);
    await settle(window, c.framingSaveMs);
    const cells = await cellsOf(gw, ps);
    const writes0 = await framingWrites(gw, token);
    const posts0 = await posts(window);

    if (dir === 'v') await gw.resizeDivider('left', -60);
    else await gw.resizeHDivider('left', -40);
    const after = (await gw.panes()).find((p) => p.id === ps[0].id)!;
    expect(dir === 'v' ? after.w : after.h, 'the divider moved').toBeLessThan(dir === 'v' ? ps[0].w - 30 : ps[0].h - 20);
    await expectCellsKept(gw, cells, 'after the divider drag');

    await settle(window, c.framingSaveMs);
    expect(await posts(window), 'a divider drag posts no SetFraming').toBe(posts0);
    expect(await framingWrites(gw, token), 'the node wrote no framing').toBe(writes0);
  });
}

test('a corner grab moves two dividers and every grid stays where it was', async ({ gw, window }) => {
  await gw.enterSource('home');
  await gw.splitFocusedPaneVertical();
  const byX = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  await gw.focusPane(byX[0]);
  await gw.splitFocusedPaneHorizontal();
  const ps = await gw.panes();
  expect(ps.length).toBe(3);
  const cells = await cellsOf(gw, ps);
  const right = ps.slice().sort((a, b) => b.x - a.x)[0];
  const lt = ps.filter((p) => p.id !== right.id).sort((a, b) => a.y - b.y)[0];
  const px = lt.x + lt.w - 3;
  const py = lt.y + lt.h - 3;
  await window.mouse.move(px, py);
  await window.mouse.down();
  expect(await window.evaluate(() => (window as any).__gridwellTest.leftResizeAxes())).toBe(2);
  await window.mouse.move(px + 50, py + 40, { steps: 10 });
  await window.mouse.up();
  await gw.waitIdle();
  await expectCellsKept(gw, cells, 'after the corner drag');
});

test('Esc mid-drag puts every view back exactly', async ({ gw, window }) => {
  const ps = await twoPanes(gw, 'v');
  const g = ps[0];
  const x = g.x + g.w - 2;
  const y = g.y + g.h / 2;
  await window.mouse.move(x, y);
  await window.mouse.down();
  await window.mouse.move(x - 80, y, { steps: 10 });
  await window.keyboard.press('Escape');
  await window.mouse.up();
  await gw.waitIdle();
  const now = await gw.panes();
  for (const p of ps) {
    const q = now.find((n) => n.id === p.id)!;
    expect([q.x, q.w, q.cx, q.cy, q.zoom], `pane ${p.id} is as it was at the press`).toEqual([p.x, p.w, p.cx, p.cy, p.zoom]);
  }
});

test('the pane that survives a crush keeps its grid where it was', async ({ gw }) => {
  const ps = await twoPanes(gw, 'v');
  const cells = await cellsOf(gw, ps.slice(1));
  await gw.collapseLeftPane();
  expect((await gw.panes()).length, 'the crush closed the left pane').toBe(1);
  await expectCellsKept(gw, cells, 'after the crush');
});

test('a window resize keeps the centre in the middle', async ({ gw, electronApp }) => {
  await gw.enterSource('home');
  const p0 = await gw.focused();
  await gw.resizeWindow(electronApp, Math.round(p0.w * 0.8), Math.round(p0.h * 0.8) + 60);
  const p1 = (await gw.panes()).find((p) => p.id === p0.id)!;
  expect(p1.w, 'the window shrank').toBeLessThan(p0.w);
  expect([p1.cx, p1.cy, p1.zoom], 'the centre stays where it was').toEqual([p0.cx, p0.cy, p0.zoom]);
});

test('inside a workspace tile a split slices the pane and the layout carries both moved centres', async ({
  gw,
  home,
  window,
}) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const c = await gw.cadences();
  await gw.enterSource('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const ws = tileAt(await gw.getGrid(f.gridID), 'workspace', wx, wy)!;
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth).toBe(1);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs));

  const p0 = await gw.focused();
  const cx = Math.round(p0.cx);
  const cy = Math.round(p0.cy);
  const before = await at(gw, p0.id, cx, cy);
  const writes0 = await framingWrites(gw, token);
  await gw.rightDragScreen(p0.x + p0.w - 5, p0.y + p0.h / 2, p0.x + p0.w * 0.75, p0.y + p0.h / 2);
  expect((await gw.panes()).length).toBe(2);
  await expectAt(gw, p0.id, cx, cy, before, 'the original keeps its cell where it was');
  const orig = (await gw.panes()).find((p) => p.id === p0.id)!;
  const made = (await gw.panes()).find((p) => p.id !== p0.id)!;
  expect(orig.cx, 'the centre moved with the boundary').not.toBeCloseTo(p0.cx, 3);
  const cell = (await at(gw, made.id, Math.round(made.cx) + 1, cy)).x - (await at(gw, made.id, Math.round(made.cx), cy)).x;
  expect(made.cx, 'the new pane shows what its rect covers').toBeCloseTo(p0.cx + (made.x + made.w / 2 - (p0.x + p0.w / 2)) / cell, 3);

  // The blob keeps each pane's views: both panes' moved centres.
  await expect
    .poll(
      async () => {
        const blob = JSON.parse(await gw.getTileContent(ws.id));
        const xs: number[] = [];
        const walk = (n: any) => {
          if (n?.pane) xs.push(...(n.pane.views ?? []).filter((v: any) => v).map((v: any) => v.x));
          if (n?.split) {
            walk(n.split.a);
            walk(n.split.b);
          }
        };
        walk(blob.root);
        return [orig.cx, made.cx].every((c) => xs.some((x) => Math.abs(x - c) < 1e-6));
      },
      { message: 'the layout written carries both moved centres', timeout: 10_000 },
    )
    .toBe(true);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs));
  expect(await framingWrites(gw, token), 'a split inside a workspace tile writes no framing').toBe(writes0);
});

test('a split below recentres the pane above: nobody dragged its boundary', async ({ gw }) => {
  await gw.enterSource('home');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  await gw.ctrlDescendCell(cx, cy, a.id);
  await expect.poll(async () => (await gw.panes()).length, { message: 'the ctrl+click split the pane' }).toBe(2);
  const aNow = (await gw.panes()).find((p) => p.id === a.id)!;
  expect(aNow.h, 'the pane above shrank').toBeLessThan(a.h);
  expect([aNow.cx, aNow.cy, aNow.zoom], 'its centre stays in its middle').toEqual([a.cx, a.cy, a.zoom]);
  const c = await gw.cellCenter(a.id, cx, cy);
  expect(c.y, 'so the door clicked stays in view').toBeCloseTo(aNow.y + aNow.h / 2 + (cy + 0.5 - aNow.cy) * 64 * aNow.zoom, 2);
});
