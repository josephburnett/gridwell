import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Inside a pane tile the panes lay out inset by the pane-tile outline, so the
// outline at the window edge lies outside every pane rect. A right-press on it
// must start an edge split exactly as one on a pane's own border does, so the
// edge target is the outline plus the pane border; see pane.GutterPane.

async function depth(window: any): Promise<number> {
  return (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
}

test('a right-press on the pane-tile outline at the window edge splits like an interior divider', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  expect(tileAt(await gw.getGrid(f.gridID), 'pane', wx, wy), 'pane tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(() => depth(window)).toBe(1);

  const winW: number = await window.evaluate(() => (globalThis as any).innerWidth);
  const one = await gw.focused();
  const rightGutter = one.x + one.w;
  expect(winW - rightGutter, 'the outline sits outside the pane at the right edge').toBeGreaterThanOrEqual(2);

  // The right window edge: the middle of the outline, dragged inward.
  const ry = one.y + one.h / 2;
  await gw.rightDragScreen(rightGutter + 1.5, ry, one.x + one.w * 0.6, ry);
  expect((await gw.panes()).length, 'right-edge outline press split a pane').toBe(2);

  // The bottom edge, above the bar row: the outline below the panes.
  const two = await gw.panes();
  const bottom = two.slice().sort((a, b) => a.x - b.x)[0];
  const bx = bottom.x + bottom.w / 2;
  await gw.rightDragScreen(bx, bottom.y + bottom.h + 1.5, bx, bottom.y + bottom.h * 0.6);
  expect((await gw.panes()).length, 'bottom-edge outline press split a pane').toBe(3);

  // Parity: the interior divider between the side-by-side panes still splits.
  const three = await gw.panes();
  const right = three.slice().sort((a, b) => b.x - a.x)[0];
  const iy = right.y + right.h / 2;
  await gw.rightDragScreen(right.x + 2, iy, right.x + right.w * 0.5, iy);
  expect((await gw.panes()).length, 'interior divider press split a pane').toBe(4);

  await gw.leaveWorkspace();
  await expect.poll(() => depth(window)).toBe(0);
});
