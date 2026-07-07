import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #77: ascending from a shell that lives INSIDE a well (a descended
// sub-grid) must persist its frozen preview. The freeze writeback resolves the
// tile against the descent path's leaf grid; a shell in a sub-grid therefore
// needs the pane's path sent with SetShellPreview — the URL twin captures it,
// the shell path historically didn't, so the save failed with "descent path is
// invalid: tile N not in path leaf grid 1" and surfaced as an error notice.
// This spec crosses the whole seam: create-in-subgrid → live PTY → ascend →
// preview persisted on the server, no error on the strip.

async function errors(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.errors());
}

test('ascending a shell inside a well persists its preview', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const wx = Math.round(home.cx);
  const wy = Math.round(home.cy);

  // A well, and a descent into its (empty) child grid.
  await gw.openPalette();
  await gw.dragCreate('well', wx, wy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', wx, wy)!;
  expect(well, 'well created').toBeTruthy();
  const child = well.childGridId as string;
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(child);

  // A shell INSIDE the well. dragCreate auto-descends and spawns the PTY.
  const inWell = await gw.focused();
  const sx = Math.round(inWell.cx);
  const sy = Math.round(inWell.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', sx, sy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shell = tileAt(await gw.getGrid(child), 'shell', sx, sy)!;
  expect(shell, 'shell created in the sub-grid').toBeTruthy();
  expect(Number(shell.previewBlobId ?? 0), 'fresh shell has no preview yet').toBe(0);

  // Ascend from the live shell (corner circle — its overlay forwards only the
  // right button). The freeze capture + SetShellPreview run on this path.
  await gw.rightClickPlus();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');

  // The preview must land on the server: the tile in the SUB-grid gains a
  // preview blob. Before the fix this write was rejected (invalid path).
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(child), 'shell', sx, sy)?.previewBlobId ?? 0), {
      timeout: 10_000,
    })
    .toBeGreaterThan(0);

  // And nothing surfaced on the error strip from the shell freeze.
  const e = await errors(window);
  expect(
    e.notices.filter((n: any) => n.source === 'shell'),
    'no shell error notice after ascent',
  ).toHaveLength(0);

  // Leave clean: delete the shell tile so its tmux session is killed and
  // teardown doesn't hang on a live PTY.
  await gw.deleteTileCell(sx, sy);
});
