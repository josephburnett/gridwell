import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// The retina hypothesis: headed Electron on macOS runs deviceScaleFactor 2,
// headless Chromium and xvfb run 1, and the headed gesture e2e fails at every
// commit with drags landing on wrong cells. If dpr=2 alone breaks gestures in
// plain Chromium, the fault is the wasm client's pointer geometry, and it is
// bisectable headlessly. Same flows as tile-gestures + workspace descent, on
// a fresh home.

test.use({ deviceScaleFactor: 2 });

test('gestures at deviceScaleFactor 2: move, resize, delete, pane descent', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  let t = tileAt(await gw.getGrid(grid), 'text', cx, cy);
  expect(t, 'created tile persisted').toBeTruthy();

  // MOVE one cell right.
  await gw.dragTileCell(cx, cy, cx + 1, cy);
  t = tileAt(await gw.getGrid(grid), 'text', cx + 1, cy);
  expect(t, 'moved tile landed one cell right').toBeTruthy();

  // RESIZE to 2x2 (drag from inside toward the far corner, as tile-gestures does).
  await gw.resizeTileCell(cx + 1, cy, cx + 2, cy + 1);
  t = tileAt(await gw.getGrid(grid), 'text', cx + 1, cy);
  expect(Number(t!.w), 'resize grew the tile wider').toBeGreaterThan(1);

  // PANE TILE: create, descend, ascend, delete.
  await gw.openPalette();
  await gw.dragCreate('pane', cx - 2, cy - 2);
  const pt = tileAt(await gw.getGrid(grid), 'pane', cx - 2, cy - 2);
  expect(pt, 'pane tile persisted').toBeTruthy();
  await gw.descendCell(cx - 2, cy - 2);
  const ws = await window.evaluate(() => (window as any).__gridwellTest.workspace());
  expect(ws.depth, 'descend into pane tile must enter the workspace').toBe(1);
  await gw.leaveWorkspace();
  await gw.waitIdle();
  await gw.deleteTileCell(cx - 2, cy - 2);
  const after = await gw.getGrid(grid);
  expect(tileAt(after, 'pane', cx - 2, cy - 2), 'trashed pane tile gone').toBeFalsy();
});
