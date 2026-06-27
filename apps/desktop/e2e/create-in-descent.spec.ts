import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Reproduces the reported bug: enter the home plugin, create a well, descend
// into it, then drag a text ("markdown") template from the palette onto a cell —
// the tile "disappears". This is the first full-stack test: it drives the real
// renderer's canvas and asserts against the real server's grid state.
//
// The control (creating the well in the root grid) and the descended create
// share one code path, so the well succeeding while the text fails isolates the
// fault to the descent leg. The oracle assertion is what pins it:
//   - text tile present in the child grid  → the create worked; any "disappear"
//     is a render/cache bug (follow up against panes()).
//   - text tile absent                      → the create request never landed
//     (wrong grid id / path / anchor, or an error reactToErr swallowed).
test('text tile dropped into a descended grid is persisted', async ({ gw }) => {
  // Enter the localdb (home) plugin from the launcher.
  await gw.enterPlugin('localdb');

  let f = await gw.focused();
  const rootGrid = f.gridID;
  expect(rootGrid, 'entered plugin should have a grid').toBeTruthy();

  // Pick cells guaranteed on-screen: near the focused pane's viewport center.
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  // CONTROL: create a well in the root grid and confirm it persisted server-side.
  await gw.openPalette();
  await gw.dragCreate('well', wx, wy);
  const rootSnap = await gw.getGrid(rootGrid);
  expect(
    tileAt(rootSnap, 'well', wx, wy),
    `control: a well should exist at (${wx},${wy}) in the root grid`,
  ).toBeTruthy();

  // Descend into the freshly-created well.
  await gw.descendCell(wx, wy);
  f = await gw.focused();
  const childGrid = f.gridID;
  expect(childGrid, 'descended pane should resolve a child grid').toBeTruthy();
  expect(childGrid, 'descent should change the framed grid').not.toBe(rootGrid);

  // THE BUG: drag a text template onto a cell in the descended grid.
  const tx = Math.round(f.cx);
  const ty = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', tx, ty);

  // ORACLE: the server must hold the text tile. (Palette "markdown" → store
  // tile kind "text".)
  const childSnap = await gw.getGrid(childGrid);
  expect(
    tileAt(childSnap, 'text', tx, ty),
    `text tile should be persisted at (${tx},${ty}) in descended grid ${childGrid}`,
  ).toBeTruthy();
});
