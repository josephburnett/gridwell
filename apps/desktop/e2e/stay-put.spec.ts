import { test, expect, homePassword, loginToken } from './fixtures';
import { settle } from './cadence';
import { tileAt } from './oracle';
import { dumpViaAPI } from './trace';
import type { GridwellDriver, PaneInfo } from './driver';

// A pane boundary the user drags covers or uncovers the grid a pane shows and
// never slides it (Joe, 2026-11-22, pane.StayPut): a split by a right-drag
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

for (const s of sides) {
  test(`a split from the ${s.side} edge leaves the original's grid where it was and writes no framing`, async ({
    gw,
    home,
    window,
  }) => {
    const token = await loginToken(gw.origin, homePassword(home));
    const c = await gw.cadences();
    await gw.enterSource('home');
    const p0 = await gw.focused();
    const cx = Math.round(p0.cx);
    const cy = Math.round(p0.cy);
    await gw.openMenu();
    await gw.dragCreate('markdown', cx, cy);
    await settle(window, c.framingSaveMs);
    const before = await at(gw, p0.id, cx, cy);
    const writes0 = await framingWrites(gw, token);
    const posts0 = await posts(window);

    const [fx, fy] = s.from(p0);
    const [tx, ty] = s.to(p0);
    await gw.rightDragScreen(fx, fy, tx, ty);
    const ps = await gw.panes();
    expect(ps.length, 'the drag split the pane').toBe(2);
    const orig = ps.find((p) => p.id === p0.id)!;
    const made = ps.find((p) => p.id !== p0.id)!;
    expect(orig.w * orig.h, 'the original shrank').toBeLessThan(p0.w * p0.h);
    await expectAt(gw, p0.id, cx, cy, before, 'the original keeps the tile where it was on screen');
    expect(orig.zoom, 'the zoom is untouched').toBe(p0.zoom);
    // The new pane is a copy of the source as it was: its centre in its middle.
    expect([made.cx, made.cy, made.zoom], 'the new pane takes the source view').toEqual([p0.cx, p0.cy, p0.zoom]);

    await settle(window, c.framingSaveMs);
    expect(await posts(window), 'a split posts no SetFraming').toBe(posts0);
    expect(await framingWrites(gw, token), 'the node wrote no framing').toBe(writes0);
  });
}

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

test('inside a workspace tile a split leaves the grid put and the layout carries the moved centre', async ({
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
  expect(orig.cx, 'the centre moved with the boundary').not.toBeCloseTo(p0.cx, 3);

  // The blob keeps each pane's views; the original's top view is the moved centre.
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
        return xs.some((x) => Math.abs(x - orig.cx) < 1e-6);
      },
      { message: 'the layout written carries the moved centre', timeout: 10_000 },
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
