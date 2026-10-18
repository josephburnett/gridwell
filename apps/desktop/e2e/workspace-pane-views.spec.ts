import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';
import { dumpViaAPI, TraceLine } from './trace';
import type { GridwellDriver, PaneInfo } from './driver';

// Inside a pane tile each pane keeps its own view of a grid, at every level
// of its stack, in the layout blob (2026-10-14). These cross from the gesture
// to the node's record of what it wrote, and from the stored blob back to
// what a reopened pane shows.

async function workspaceDepth(window: any): Promise<number> {
  return (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
}

function storeWrites(lines: TraceLine[], verb: (msg: string) => boolean): number {
  return lines.filter((l) => l.src === 'store' && l.kind === 'write' && verb(l.msg)).length;
}
const isFraming = (m: string) => m.startsWith('SetFraming');

type View = { cx: number; cy: number; zoom: number };

function byX(ps: PaneInfo[]): PaneInfo[] {
  return ps.slice().sort((a, b) => a.x - b.x);
}

async function expectViews(gw: GridwellDriver, want: View[], why: string): Promise<void> {
  await expect
    .poll(async () => (await gw.panes()).length, { message: `${why}: both panes back`, timeout: 15_000 })
    .toBe(want.length);
  for (let i = 0; i < want.length; i++) {
    await expect
      .poll(async () => byX(await gw.panes())[i].zoom, { message: `${why}: pane ${i} zoom` })
      .toBeCloseTo(want[i].zoom, 2);
    const p = byX(await gw.panes())[i];
    expect(p.cx, `${why}: pane ${i} center x`).toBeCloseTo(want[i].cx, 1);
    expect(p.cy, `${why}: pane ${i} center y`).toBeCloseTo(want[i].cy, 1);
  }
}

async function reopenByReload(window: any, tileID: string): Promise<void> {
  await window.evaluate(
    ([id]: string[]) => {
      location.href = `${location.origin}/?w=${encodeURIComponent(id)}&e2e=1`;
    },
    [tileID],
  );
  await window.waitForFunction(() => (window as any).__gridwellTest !== undefined);
  await expect.poll(() => workspaceDepth(window), { timeout: 15_000 }).toBe(1);
}

test('two panes on one grid keep their own views through leave, re-entry and reload; the doorway row is unmoved', async ({
  gw,
  home,
  window,
}) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  await gw.openMenu();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;
  const wellBefore = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy)!;
  expect(pt && wellBefore, 'pane tile and well persisted').toBeTruthy();

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.descendCell(wx + 1, wy);
  const childGrid = (await gw.focused()).gridID;
  expect(childGrid).not.toBe(rootGrid);
  await gw.splitFocusedPaneVertical();
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs));
  const framingBefore = storeWrites(await dumpViaAPI(gw.origin, token), isFraming);

  // The same grid, two different views: one zoomed in and panned up-left, the
  // other zoomed out and panned down-left. The child is empty, so no press
  // lands on a tile.
  const [left, right] = byX(await gw.panes());
  await gw.focusPane(left);
  await gw.wheelAtFocusedCenter(-300);
  let z = await gw.focused();
  await gw.panFocusedGrid(Math.round(z.cx), Math.round(z.cy), Math.round(z.cx) - 1, Math.round(z.cy) - 1);
  await gw.focusPane(right);
  await gw.wheelAtFocusedCenter(200);
  z = await gw.focused();
  await gw.panFocusedGrid(Math.round(z.cx), Math.round(z.cy), Math.round(z.cx) - 1, Math.round(z.cy) + 1);
  const want = byX(await gw.panes()).map((p) => ({ cx: p.cx, cy: p.cy, zoom: p.zoom }));
  expect(want[0].zoom, 'the two panes hold different zooms').not.toBeCloseTo(want[1].zoom, 1);

  await expect
    .poll(async () => (await gw.getTileContent(pt.id)).includes('"views"'), {
      message: 'the settled views reached the layout blob',
      timeout: 10_000,
    })
    .toBe(true);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs));
  expect(storeWrites(await dumpViaAPI(gw.origin, token), isFraming), 'a pan inside a pane tile wrote SetFraming')
    .toBe(framingBefore);

  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  const wellAfter = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy)!;
  expect([wellAfter.viewCx, wellAfter.viewCy, wellAfter.viewZoom], 'the doorway row moved')
    .toEqual([wellBefore.viewCx, wellBefore.viewCy, wellBefore.viewZoom]);

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expectViews(gw, want, 're-entry');

  await reopenByReload(window, pt.id);
  await expectViews(gw, want, 'reload');
  await gw.leaveWorkspace();
  const wellEnd = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy)!;
  expect([wellEnd.viewCx, wellEnd.viewCy, wellEnd.viewZoom], 'the doorway row moved by the visit')
    .toEqual([wellBefore.viewCx, wellBefore.viewCy, wellBefore.viewZoom]);
});

test('a reopened pane ascends onto the view it left the level below at', async ({ gw, window }) => {
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  await gw.openMenu();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.descendCell(wx + 1, wy);
  const outerGrid = (await gw.focused()).gridID;

  // Frame the outer grid away from its row's framing, then make a well at
  // its center and go through it.
  await gw.wheelAtFocusedCenter(-200);
  let z = await gw.focused();
  await gw.panFocusedGrid(Math.round(z.cx), Math.round(z.cy), Math.round(z.cx) - 2, Math.round(z.cy) - 1);
  z = await gw.focused();
  const ix = Math.round(z.cx);
  const iy = Math.round(z.cy);
  await gw.openMenu();
  await gw.dragCreate('well', ix, iy);
  const outer = await gw.focused();
  const outerView = { cx: outer.cx, cy: outer.cy, zoom: outer.zoom };
  await gw.descendCell(ix, iy);
  const innerGrid = (await gw.focused()).gridID;
  expect(innerGrid).not.toBe(outerGrid);
  await gw.panFocusedGrid(0, 0, 1, 1);
  const inner = await gw.focused();

  await expect
    .poll(async () => (await gw.getTileContent(pt.id)).includes('"views"'), { timeout: 10_000 })
    .toBe(true);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs));

  for (const how of ['re-entry', 'reload']) {
    if (how === 're-entry') {
      await gw.leaveWorkspace();
      await expect.poll(() => workspaceDepth(window)).toBe(0);
      await gw.descendCell(wx, wy);
      await expect.poll(() => workspaceDepth(window)).toBe(1);
    } else {
      await reopenByReload(window, pt.id);
    }
    await expect.poll(async () => (await gw.focused()).gridID, { message: `${how}: inner grid` }).toBe(innerGrid);
    await expectViews(gw, [{ cx: inner.cx, cy: inner.cy, zoom: inner.zoom }], `${how}: inner`);
    await gw.middleClickPane();
    await expect.poll(async () => (await gw.focused()).gridID, { message: `${how}: ascended` }).toBe(outerGrid);
    await expectViews(gw, [outerView], `${how}: the level below`);
    // Back down, so the next round reopens two deep.
    await gw.descendCell(ix, iy);
    await expect.poll(async () => (await gw.focused()).gridID).toBe(innerGrid);
    await gw.panFocusedGrid(0, 0, 1, 1);
    const again = await gw.focused();
    inner.cx = again.cx;
    inner.cy = again.cy;
    inner.zoom = again.zoom;
    await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs) * 2);
  }
  await gw.leaveWorkspace();
});
