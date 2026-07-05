import { test, expect } from './fixtures';

// Locks the descend → reframe → ascend round trip: invariant I7, "preview =
// descent target = ascent return" (CLAUDE.md face #3; ARCHITECTURE.md §11). This
// is the invariant the owner's "previews go wonky" reports are about, and the
// test forensics found it had no test home at all — the framing round trip was
// asserted nowhere. It is also the safety net any framing refactor (Phase 1b)
// must have first: these tests must stay green while the framing copies are given
// a single owner.
//
// Both tests drive the real app and read framing from the panes() hook (the live
// viewport the user sees), so they catch a desync between the live pane, the
// saved ascent state, and the server-persisted well view_*.
//
// The plugin-root viewport tests (I7-portal) lock invariant I7 at the
// launcher↔plugin-root seam: enter a plugin, pan/zoom its root grid, ascend to
// the launcher, re-enter — the viewport must be exactly as left.  This seam
// was previously untested (framing-roundtrip only covered wells inside a
// plugin) and was broken: every re-entry reset to the default calibrated zoom.

test('re-descending a reframed well returns to exactly what you left', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  const childGrid = (await gw.focused()).gridID;

  // Reframe the child grid: zoom in, then pan off-center. The child is empty, so
  // the press lands on no tile and pans.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'the reframe actually changed the zoom').not.toBeCloseTo(1.0, 2);

  // Ascend out, then re-descend: must land on exactly the framing we left.
  await gw.middleClickCell(Math.round(left.cx), Math.round(left.cy));
  expect((await gw.focused()).gridID, 'ascended out of the child').not.toBe(childGrid);

  await gw.descendCell(cx, cy);
  const back = await gw.focused();
  expect(back.gridID, 're-descended into the same child grid').toBe(childGrid);
  expect(back.zoom, 'zoom round-tripped').toBeCloseTo(left.zoom, 1);
  expect(back.cx, 'center x round-tripped').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y round-tripped').toBeCloseTo(left.cy, 1);
});

test('plugin root-grid viewport persists across launcher ascent and re-entry', async ({ gw }) => {
  // Invariant: enter a plugin, reframe its root grid, ascend to the launcher,
  // re-enter — viewport must match what was left (issue #32).
  await gw.enterPlugin('localdb');
  const pluginGrid = (await gw.focused()).gridID;

  // Reframe the plugin root: zoom in, then pan. The grid is empty so the
  // press lands on nothing and pans.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'reframe actually changed the zoom').not.toBeCloseTo(1.0, 2);

  // Ascend back to the launcher.
  await gw.rightClickPlus();
  const launcher = await gw.focused();
  expect(launcher.gridID, 'ascended to the launcher').not.toBe(pluginGrid);

  // Re-enter the plugin: viewport must match what we left.
  await gw.enterPlugin('localdb');
  const back = await gw.focused();
  expect(back.gridID, 're-entered the same plugin root').toBe(pluginGrid);
  expect(back.zoom, 'zoom restored after re-entry').toBeCloseTo(left.zoom, 1);
  expect(back.cx, 'center x restored after re-entry').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y restored after re-entry').toBeCloseTo(left.cy, 1);
});

test('ascending restores the parent viewport unchanged', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);

  // The parent (root) framing immediately before descending.
  const before = await gw.focused();

  await gw.descendCell(cx, cy);
  const childGrid = (await gw.focused()).gridID;
  // Reframe the child so ascent has a real framing to write back.
  await gw.wheelAtFocusedCenter(-200);
  const inside = await gw.focused();
  await gw.middleClickCell(Math.round(inside.cx), Math.round(inside.cy));

  const after = await gw.focused();
  expect(after.gridID, 'back in the parent grid').not.toBe(childGrid);
  expect(after.zoom, 'parent zoom unchanged by the descent excursion').toBeCloseTo(before.zoom, 1);
  expect(after.cx, 'parent center x unchanged').toBeCloseTo(before.cx, 1);
  expect(after.cy, 'parent center y unchanged').toBeCloseTo(before.cy, 1);
});
