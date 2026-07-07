import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #83: after any ascent, an ephemeral yellow outline marks the tile the
// pane just came out of — so the user can tell WHICH shell/well they just left
// before, say, throwing one away. The trace arms when the ascent transition
// lands, fades over ~2s, and expires; strictly view state, nothing persists.

async function traces(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.traces());
}

test('ascending arms a fading trace on the tile just left, then it expires', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;

  // Descend into the well, then ascend back out.
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(well.childGridId);
  await gw.middleClickCell(cx, cy);
  await gw.waitIdle();

  // The trace is armed on the well tile in the focused pane, alpha near 1.
  const armed = await traces(window);
  expect(armed.length, 'one trace armed after the ascent').toBe(1);
  expect(armed[0].tileId, 'trace points at the well just left').toBe(well.id);
  expect(armed[0].paneId).toBe((await gw.focused()).id);
  expect(armed[0].alpha).toBeGreaterThan(0.3);

  // And it expires on its own (~2s fade), leaving no residue.
  await expect.poll(async () => (await traces(window)).length, { timeout: 5_000 }).toBe(0);

  // The guiding rule: the trace changed nothing — the well is byte-identical.
  const after = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(after.version, 'no version bump from the trace').toBe(well.version);
});
