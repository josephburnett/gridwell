import { expect } from '@playwright/test';
import { GridwellDriver } from './driver';
import { tileAt } from './oracle';
import { settle } from './cadence';
import { dumpNow, TraceLine } from './trace';

// The 2026-10-04 trace, as a scenario both renderers run: inside a pane tile,
// two panes on different grids, one zoomed to fill through the bar title so
// the other has no rect, then a reload, which restores both leaves pending
// their owner rows' views. The hidden pane must neither adopt a view against
// its missing rect nor write one, and once it is shown again a wheel in it
// writes a framing that is one. It wrote zoom 0 over the well's saved view on
// every settle, and its next wheel stored NaN.

interface Persist {
  tile: string;
  cx: number;
  cy: number;
  zoom: number;
}

// The client's framing writes (traceevent.Framing), parsed.
function persists(lines: TraceLine[], cid: string): Persist[] {
  return lines
    .filter((l) => l.origin === 'client' && l.cid === cid && l.src === 'framing' && l.kind === 'persist')
    .map((l) => {
      const m = /center (\S+),(\S+) zoom (\S+)/.exec(l.msg)!;
      return { tile: l.kv?.tile ?? '', cx: Number(m[1]), cy: Number(m[2]), zoom: Number(m[3]) };
    });
}

function isView(p: { cx: number; cy: number; zoom: number }): boolean {
  return Number.isFinite(p.cx) && Number.isFinite(p.cy) && Number.isFinite(p.zoom) && p.zoom > 0;
}

async function errorNotices(window: any): Promise<string[]> {
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  return e.notices.filter((n: any) => n.severity === 'error').map((n: any) => `${n.source}: ${n.message}`);
}

async function workspaceDepth(window: any): Promise<number> {
  return (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
}

export async function hiddenPaneScenario(gw: GridwellDriver, window: any, token: string): Promise<void> {
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  await gw.openPalette();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;
  const well = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy)!;
  expect(pt && well, 'pane tile and well persisted').toBeTruthy();

  // Give the well a saved view of its own, outside the pane tile.
  await gw.descendCell(wx + 1, wy);
  await gw.wheelAtFocusedCenter(-300);
  const framed = await gw.focused();
  await gw.middleClickCell(Math.round(framed.cx), Math.round(framed.cy));
  const savedView = async () => {
    const t = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy) as any;
    return { cx: Number(t?.viewCx ?? 0), cy: Number(t?.viewCy ?? 0), zoom: Number(t?.viewZoom ?? 0) };
  };
  await expect.poll(async () => (await savedView()).zoom, { message: 'the ascent framed the well' })
    .toBeGreaterThan(0);

  // Two panes on different grids: the root, and the well's grid.
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.splitFocusedPaneVertical();
  await gw.descendCell(wx + 1, wy);
  const inWell = await gw.focused();
  expect(inWell.gridID, 'one pane is inside the well').toBe(well.childGridId);
  const onRoot = (await gw.panes()).find((p) => p.id !== inWell.id)!;
  expect(onRoot.gridID, 'the other is on the root').toBe(rootGrid);
  await settle(window, c.framingSaveMs);
  const saved = await savedView();

  // Zoom the root pane to fill: the well's pane is laid out nowhere.
  await gw.focusPane(onRoot);
  await gw.clickBarName();
  await gw.waitIdle();
  expect((await gw.panes()).map((p) => p.id), 'only the zoomed pane is laid out').toEqual([onRoot.id]);
  await expect
    .poll(async () => (await gw.getTileContent(pt.id)).includes('"zoomed"'), {
      message: 'the layout blob records the zoom',
      timeout: 10_000,
    })
    .toBe(true);

  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(() => workspaceDepth(window), { timeout: 30_000 }).toBe(1);
  await gw.waitIdle();
  expect((await gw.panes()).map((p) => p.id), 'the reload restored the zoom').toEqual([onRoot.id]);
  // Settle windows enough for the hidden pane to have written, had it been a
  // writer: the trace's zero write repeated on every one.
  for (let i = 0; i < 3; i++) await settle(window, c.framingSaveMs);

  const hidden = await dumpNow(window, gw.origin, token);
  const whileHidden = persists(hidden.lines, hidden.cid);
  expect(
    whileHidden.filter((p) => p.tile === well.id),
    'the hidden pane wrote the well framing',
  ).toEqual([]);
  expect(whileHidden.filter((p) => !isView(p)), 'a framing write that is not a view').toEqual([]);
  const held = await savedView();
  expect(held.zoom, "the well's saved view was erased").toBeCloseTo(saved.zoom, 3);
  expect(held.cx).toBeCloseTo(saved.cx, 3);
  expect(held.cy).toBeCloseTo(saved.cy, 3);

  // Back to two panes, and a wheel in the one that was hidden.
  await gw.clickBarName();
  await gw.waitIdle();
  const shown = (await gw.panes()).find((p) => p.id === inWell.id);
  expect(shown, 'the hidden pane is laid out again').toBeTruthy();
  expect(isView(shown!), `the shown pane's view: ${JSON.stringify(shown)}`).toBe(true);
  await window.mouse.move(shown!.x + shown!.w / 2, shown!.y + shown!.h / 2);
  await window.mouse.wheel(0, -120);
  await gw.waitIdle();
  for (let i = 0; i < 2; i++) await settle(window, c.framingSaveMs);

  const after = await dumpNow(window, gw.origin, token);
  const all = persists(after.lines, after.cid);
  expect(all.filter((p) => !isView(p)), 'a framing write that is not a view').toEqual([]);
  const wheeled = await savedView();
  expect(isView(wheeled), `the well's view after the wheel: ${JSON.stringify(wheeled)}`).toBe(true);
  expect(await errorNotices(window), 'an error notice').toEqual([]);
  await gw.leaveWorkspace();
}
