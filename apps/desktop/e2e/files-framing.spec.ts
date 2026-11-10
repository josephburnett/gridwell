import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import type { GridwellDriver, PaneInfo } from './driver';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// A directory's framing is on the row that owns its door, as a home door's
// is (framing-roundtrip.spec.ts), so a directory reframed and left reopens
// exactly where it was left, by every way into it: a door under a root, two
// levels down, a root from the + menu, and a link on home to either.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub', 'deeper'), { recursive: true });
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

async function doorTo(gw: GridwellDriver, gridID: string, name: string): Promise<any> {
  return ((await gw.getGrid(gridID)).tiles ?? []).find((t: any) => t.altText === name);
}

// Zooms and pans the focused pane over cells no tile holds, so the wheel
// zooms the pane rather than a door's preview (gesture.ClassifyWheel).
async function reframe(gw: GridwellDriver, win: Page, notches = 1): Promise<PaneInfo> {
  const p = await gw.focused();
  const empty = await gw.cellCenter(p.id, Math.round(p.cx) + 1, Math.round(p.cy) + 1);
  await win.mouse.move(empty.x, empty.y);
  await win.mouse.wheel(0, -200 * notches);
  const z = await gw.focused();
  await gw.panFocusedGrid(Math.round(z.cx) + 1, Math.round(z.cy) + 1, Math.round(z.cx), Math.round(z.cy));
  const left = await gw.focused();
  expect(left.zoom, 'the wheel zoomed the pane').not.toBeCloseTo(p.zoom, 2);
  return left;
}

function expectSameView(back: PaneInfo, left: PaneInfo, what: string): void {
  expect(back.zoom, `${what}: zoom`).toBeCloseTo(left.zoom, 1);
  expect(back.cx, `${what}: center x`).toBeCloseTo(left.cx, 1);
  expect(back.cy, `${what}: center y`).toBeCloseTo(left.cy, 1);
}

// Descends through the door at (x, y), reframes, ascends, and descends again,
// twice, so the second round starts from a row the first one wrote.
async function roundTrip(gw: GridwellDriver, win: Page, x: number, y: number, between?: () => Promise<void>) {
  const parent = (await gw.focused()).gridID;
  for (let round = 0; round < 2; round++) {
    await gw.descendCell(x, y);
    const inside = (await gw.focused()).gridID;
    expect(inside, 'descended').not.toBe(parent);
    const left = await reframe(gw, win, round + 1);
    await gw.ascendViaCrumb();
    expect((await gw.focused()).gridID, 'ascended').toBe(parent);
    await gw.waitIdle();
    if (between) await between();
    await gw.descendCell(x, y);
    const back = await gw.focused();
    expect(back.gridID, 'the same grid').toBe(inside);
    if (between) {
      // The stored zoom is against the pane, so a resized pane shows the
      // same share of it (zoomtrans.Writeback).
      expect(back.zoom / back.w, `round ${round}: zoom per pane width`).toBeCloseTo(left.zoom / left.w, 4);
      expect(back.cx, `round ${round}: center x`).toBeCloseTo(left.cx, 1);
      expect(back.cy, `round ${round}: center y`).toBeCloseTo(left.cy, 1);
    } else {
      expectSameView(back, left, `round ${round}`);
    }
    await gw.ascendViaCrumb();
    await gw.waitIdle();
  }
}

test('a directory under a root reopens as it was left, and its row keeps it', async ({ gw, window }) => {
  await gw.sources();
  await gw.enterSource('docs (files)');
  const root = await gw.focused();
  const sub = await doorTo(gw, root.gridID, 'sub');
  await roundTrip(gw, window, Number(sub.x ?? 0), Number(sub.y ?? 0));
  expect(Number((await doorTo(gw, root.gridID, 'sub')).viewZoom ?? 0), "the node's row carries it").toBeGreaterThan(0);
});

test('two levels down, across a window resize', async ({ gw, window, electronApp }) => {
  await gw.sources();
  await gw.enterSource('docs (files)');
  const sub = await doorTo(gw, (await gw.focused()).gridID, 'sub');
  await gw.descendCell(Number(sub.x ?? 0), Number(sub.y ?? 0));
  const deeper = await doorTo(gw, (await gw.focused()).gridID, 'deeper');
  let w = 1180;
  await roundTrip(gw, window, Number(deeper.x ?? 0), Number(deeper.y ?? 0), async () => {
    await gw.resizeWindow(electronApp, w, 720);
    w -= 100;
  });
});

test('a files root reopens from the + menu as it was left', async ({ gw, window }) => {
  await gw.sources();
  await gw.enterSource('home');
  for (let round = 0; round < 2; round++) {
    await gw.enterSource('docs (files)');
    const left = await reframe(gw, window, round + 1);
    await gw.ascendViaCrumb();
    await gw.waitIdle();
    await gw.enterSource('docs (files)');
    expectSameView(await gw.focused(), left, `round ${round}`);
    await gw.ascendViaCrumb();
  }
});

test('a link on home to a root, and one to a directory, each keep their own', async ({ gw, window }) => {
  await gw.sources();
  await gw.splitFocusedPaneVertical();
  const [l0, r0] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(l0);
  await gw.enterSource('home');
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === r0.id)!);
  await gw.enterSource('docs (files)');
  const dir = (await gw.panes()).find((p: any) => p.id === r0.id)!;
  const home = (await gw.panes()).find((p: any) => p.id === l0.id)!;
  const sub = await doorTo(gw, dir.gridID, 'sub');
  const [hx, hy] = [Math.round(home.cx), Math.round(home.cy) - 1];
  await gw.copyDragAcrossPanes(dir.id, Number(sub.x ?? 0), Number(sub.y ?? 0), home.id, hx, hy);
  expect(tileAt(await gw.getGrid(home.gridID), 'door', hx, hy)?.link, 'a link to sub').toBe(true);
  await gw.focusPane(home);
  await gw.openMenu();
  await gw.dragSourceLink('docs (files)', hx, hy + 1);
  expect(tileAt(await gw.getGrid(home.gridID), 'door', hx, hy + 1), 'a door to the root').toBeTruthy();

  await roundTrip(gw, window, hx, hy);
  await roundTrip(gw, window, hx, hy + 1);
});

test('inside a workspace tile, a directory and a home door each reopen as left', async ({ gw, window }) => {
  await gw.sources();
  await gw.enterSource('home');
  const f = await gw.focused();
  const [wx, wy] = [Math.round(f.cx), Math.round(f.cy) - 1];
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  await gw.descendCell(wx, wy);
  await gw.waitIdle();
  const inner = await gw.focused();
  const [dx, dy] = [Math.round(inner.cx), Math.round(inner.cy)];
  await gw.openMenu();
  await gw.dragCreate('door', dx, dy);
  await roundTrip(gw, window, dx, dy);

  await gw.enterSource('docs (files)');
  const sub = await doorTo(gw, (await gw.focused()).gridID, 'sub');
  await roundTrip(gw, window, Number(sub.x ?? 0), Number(sub.y ?? 0));
});
