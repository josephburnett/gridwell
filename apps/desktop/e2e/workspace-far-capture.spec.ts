import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { tileAt } from './oracle';

// A workspace tile on a far node, made from panes that reached that node
// through this node's own door, keeps every pane where it was: a layout is
// written in its owning node's frame, so each pane's place is respelled from
// the first frame that node can name (pane.Stack.Respelled), never dropped.
// A pane the far node cannot name at all, one on this node's own grid, opens
// at the far node's home, and that is told once. Joe's trace, 2026-11-22.

test.use({ extraNodes: ['second'] });

const depth = (window: Page): Promise<number> =>
  window.evaluate(() => (window as any).__gridwellTest.workspace().depth);

const layoutNotices = async (window: Page): Promise<{ message: string; count: number }[]> =>
  (((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []) as any[]).filter((n) =>
    String(n.source).startsWith('layout:'),
  );

const byX = <T extends { x: number }>(ps: T[]): T[] => ps.slice().sort((a, b) => a.x - b.x);

// throughHomeDoor puts a link to the far home on this node's home, descends
// through it and splits, so both panes' places begin on this node.
async function throughHomeDoor(gw: any) {
  const rows = await gw.sources();
  const home = rows[0].rootGridID;
  const far = rows.find((r: any) => r.label === 'second').rootGridID;
  const f = await gw.focused();
  const cx = Math.round(f.cx) + 1;
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragSourceLink('second', cx, cy);
  expect(tileAt(await gw.getGrid(home), 'door', cx, cy), 'the door to the far home').toBeTruthy();
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(far);
  await gw.splitFocusedPaneVertical();
  return { home, far };
}

async function captured(gw: any, tileID: string): Promise<void> {
  await expect
    .poll(async () => (await gw.getTileContent(tileID).catch(() => '')).includes('"split"'), { timeout: 10_000 })
    .toBe(true);
}

test('a far workspace keeps panes that came through this node, and reopens them', async ({ gw, window }) => {
  const { far } = await throughHomeDoor(gw);
  const before = byX(await gw.panes());
  expect(before.map((p) => [p.gridID, p.placeDepth]), 'two panes one door below home').toEqual([
    [far, 1],
    [far, 1],
  ]);

  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', cx, cy);
  const ws = tileAt(await gw.getGrid(far), 'workspace', cx, cy)!;
  expect(ws, 'the workspace tile is on the far home').toBeTruthy();
  await gw.descendCell(cx, cy);
  await expect.poll(async () => depth(window)).toBe(1);

  const inner = byX(await gw.panes());
  expect(
    inner.map((p) => [p.gridID, p.anchor, p.placeDepth]),
    "each pane shows the far home, anchored there, this node's frame left out",
  ).toEqual([
    [far, far, 0],
    [far, far, 0],
  ]);
  for (const [i, p] of inner.entries()) {
    expect(p.zoom, `pane ${i} keeps its zoom`).toBeCloseTo(before[i].zoom, 6);
    expect(p.cx, `pane ${i} keeps its center`).toBeCloseTo(before[i].cx, 6);
    expect(p.cy, `pane ${i} keeps its center`).toBeCloseTo(before[i].cy, 6);
  }
  await captured(gw, ws.id);
  const blob = await gw.getTileContent(ws.id);
  expect(blob, 'the layout names no id of this node').not.toContain(far.split('/')[0]);
  expect(await layoutNotices(window), 'nothing was dropped, so nothing is told').toEqual([]);

  await gw.leaveWorkspace();
  await expect.poll(async () => depth(window)).toBe(0);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => depth(window)).toBe(1);
  await expect
    .poll(async () => byX(await gw.panes()).map((p) => [p.gridID, p.placeDepth]))
    .toEqual([
      [far, 0],
      [far, 0],
    ]);
  expect(await layoutNotices(window), 'and reopening tells nothing').toEqual([]);
});

test("a pane on this node's own grid opens at the far node's home, told once", async ({ gw, window }) => {
  const { home, far } = await throughHomeDoor(gw);
  const [a0, b0] = byX(await gw.panes());

  // Pane A goes back up to this node's home, which the far node cannot name.
  await gw.focusPane(a0);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(home);

  // Pane B goes one door deeper on the far node and makes the workspace there.
  await gw.focusPane(b0);
  const b = await gw.focused();
  const dx = Math.round(b.cx);
  const dy = Math.round(b.cy) - 1;
  await gw.openMenu();
  await gw.dragCreate('door', dx, dy);
  const door = tileAt(await gw.getGrid(far), 'door', dx, dy)!;
  expect(door, 'a door on the far home').toBeTruthy();
  await gw.descendCell(dx, dy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(door.childGridId);
  const inside = await gw.focused();
  const wx = Math.round(inside.cx);
  const wy = Math.round(inside.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const ws = tileAt(await gw.getGrid(door.childGridId!), 'workspace', wx, wy)!;
  expect(ws, 'the workspace tile is on the far door').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => depth(window)).toBe(1);

  await expect
    .poll(async () => byX(await gw.panes()).map((p) => [p.gridID, p.placeDepth]), {
      message: "pane A at the far node's home, pane B where it was",
    })
    .toEqual([
      [far, 0],
      [door.childGridId, 1],
    ]);
  await captured(gw, ws.id);
  await expect.poll(async () => (await layoutNotices(window)).map((n) => n.count), { message: 'told once' }).toEqual([1]);

  await gw.leaveWorkspace();
  await expect.poll(async () => depth(window)).toBe(0);
  await gw.descendCell(wx, wy);
  await expect.poll(async () => depth(window)).toBe(1);
  await expect
    .poll(async () => byX(await gw.panes()).map((p) => [p.gridID, p.placeDepth]))
    .toEqual([
      [far, 0],
      [door.childGridId, 1],
    ]);
  await gw.waitIdle();
  const counts = (await layoutNotices(window)).map((n) => n.count);
  expect(Math.max(0, ...counts), 'a save that drops nothing tells nothing').toBeLessThanOrEqual(1);
});
