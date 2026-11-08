import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { setSelection, tileAt } from './oracle';
import type { GridwellDriver } from './driver';

// Shift + left-click on a crumb in the bar toggles the tile that crumb stands
// for in the selection of the grid it sits on, the same write a shift-click on
// the tile makes, and never ascends or leaves a level. A selected tile's
// crumb wears the selection border.

async function onNode(gw: GridwellDriver, gridID: string): Promise<string[]> {
  return ((await gw.getGrid(gridID)).grid?.selection ?? []).slice().sort();
}

async function crumbOf(gw: GridwellDriver, tileID: string): Promise<any> {
  const seg = (await gw.bar()).segments.find((s: any) => s.tileID === tileID);
  expect(seg, `the bar has a crumb for ${tileID}`).toBeTruthy();
  return seg;
}

// The pixel just inside the crumb's left edge, halfway down: inset is where the
// crumb's square or bar starts inside its segment (drawChainCrumb, 1;
// drawBoundaryCrumb, 2).
async function crumbEdgePixel(gw: GridwellDriver, win: Page, tileID: string, inset: number): Promise<number[]> {
  const b = await gw.bar();
  const seg = await crumbOf(gw, tileID);
  return win.evaluate(
    ([px, py]) => {
      const canvas = document.querySelector('canvas')!;
      const r = canvas.getBoundingClientRect();
      const sx = canvas.width / r.width;
      const d = canvas.getContext('2d')!.getImageData(Math.round(px * sx), Math.round(py * sx), 1, 1).data;
      return [d[0], d[1], d[2]];
    },
    [seg.x + inset + 1.5, b.top + b.height / 2],
  );
}

const yellow = [0xf7, 0xea, 0x1c];

async function shiftClickCrumb(gw: GridwellDriver, win: Page, tileID: string) {
  const b = await gw.bar();
  const seg = await crumbOf(gw, tileID);
  await win.keyboard.down('Shift');
  await win.mouse.click(seg.x + seg.w / 2, b.top + b.height / 2);
  await win.keyboard.up('Shift');
  await gw.waitIdle();
}

test('shift-click on a door crumb toggles the door in its grid and stays where the pane is', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f0 = await gw.focused();
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const door = tileAt(await gw.getGrid(f0.gridID), 'door', cx, cy)!;
  expect(door, 'door created').toBeTruthy();
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).not.toBe(f0.gridID);
  const f1 = await gw.focused();

  expect(await crumbEdgePixel(gw, window, door.id, 1), 'an unselected crumb has no yellow').not.toEqual(yellow);
  await shiftClickCrumb(gw, window, door.id);
  await expect.poll(() => onNode(gw, f0.gridID), { message: 'the door is selected on its grid' }).toEqual([door.id]);
  await expect.poll(async () => (await crumbOf(gw, door.id)).selected, { message: 'the crumb drew the border' }).toBe(true);
  expect(await crumbEdgePixel(gw, window, door.id, 1), 'the border wears the theme yellow').toEqual(yellow);
  let now = await gw.focused();
  expect(now.gridID, 'the shift-click did not ascend').toBe(f1.gridID);
  expect(now.placeDepth, 'the shift-click did not ascend').toBe(f1.placeDepth);
  expect(await onNode(gw, f1.gridID), 'the grid the pane is on is not touched').toEqual([]);

  await shiftClickCrumb(gw, window, door.id);
  await expect.poll(() => onNode(gw, f0.gridID), { message: 'a second shift-click takes it out' }).toEqual([]);
  await expect.poll(async () => (await crumbOf(gw, door.id)).selected, { message: 'the border went' }).toBe(false);
  now = await gw.focused();
  expect(now.gridID, 'still where it was').toBe(f1.gridID);

  // A shift-click on the grid crumb at the root does nothing.
  const root = (await gw.bar()).segments.find((s: any) => s.kind === 'chain' && s.anchor === f0.gridID)!;
  expect(root, 'the root crumb').toBeTruthy();
  const b = await gw.bar();
  await window.keyboard.down('Shift');
  await window.mouse.click(root.x + root.w / 2, b.top + b.height / 2);
  await window.keyboard.up('Shift');
  await gw.waitIdle();
  now = await gw.focused();
  expect(now.gridID, 'a shift-click on a grid crumb does not ascend').toBe(f1.gridID);
  expect(await onNode(gw, f0.gridID), 'nor selects anything').toEqual([]);
});

test('shift-click on a workspace tile crumb toggles the workspace tile and stays inside it', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f0 = await gw.focused();
  const wx = Math.round(f0.cx);
  const wy = Math.round(f0.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const ws = tileAt(await gw.getGrid(f0.gridID), 'workspace', wx, wy)!;
  expect(ws, 'workspace tile created').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await gw.bar()).segments.some((s: any) => s.kind === 'workspace')).toBe(true);

  await shiftClickCrumb(gw, window, ws.id);
  await expect.poll(() => onNode(gw, f0.gridID), { message: 'the workspace tile is selected on its grid' }).toEqual([
    ws.id,
  ]);
  expect((await crumbOf(gw, ws.id)).kind, 'the window is still inside the workspace').toBe('workspace');
  await expect.poll(async () => (await crumbOf(gw, ws.id)).selected, { message: 'the crumb drew the border' }).toBe(true);
  expect(await crumbEdgePixel(gw, window, ws.id, 2), 'the border wears the theme yellow').toEqual(yellow);

  await shiftClickCrumb(gw, window, ws.id);
  await expect.poll(() => onNode(gw, f0.gridID), { message: 'a second shift-click takes it out' }).toEqual([]);
  expect((await crumbOf(gw, ws.id)).kind, 'still inside').toBe('workspace');
  await expect.poll(async () => (await crumbOf(gw, ws.id)).selected, { message: 'the border went' }).toBe(false);
});

// Another writer's select reaches the crumb's border without a gesture: the
// view lists the grid the crumb sits on, so its SelectionChanged arrives.
test("a select made elsewhere of a crumb's tile reaches its border", async ({ gw }) => {
  await gw.enterSource('home');
  const f0 = await gw.focused();
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const door = tileAt(await gw.getGrid(f0.gridID), 'door', cx, cy)!;
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).not.toBe(f0.gridID);

  await setSelection(gw.origin, f0.gridID, [door.id]);
  await expect.poll(async () => (await crumbOf(gw, door.id)).selected, { message: 'told, the crumb drew the border' }).toBe(true);
  await setSelection(gw.origin, f0.gridID, [], [door.id]);
  await expect.poll(async () => (await crumbOf(gw, door.id)).selected, { message: 'told, the border went' }).toBe(false);
});
