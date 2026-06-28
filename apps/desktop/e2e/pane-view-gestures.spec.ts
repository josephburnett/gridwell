import { test, expect } from './fixtures';

// Drives the pane-management and view-framing gestures. These have no server
// footprint (they reframe/relayout the client), so the oracle here is the
// read-only panes() hook: pane count for split, viewport center for pan, zoom
// for wheel, and the framed grid id / path for ascend.

test('wheel zoom and drag-pan reframe the focused pane', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const before = await gw.focused();

  // Wheel up (negative dy) zooms in: the focused pane's zoom changes.
  await gw.wheelAtFocusedCenter(-300);
  const zoomed = await gw.focused();
  expect(zoomed.zoom, 'wheel changed the zoom').not.toBeCloseTo(before.zoom, 3);

  // Drag-pan from one empty cell to another shifts the viewport center. The
  // root grid is empty after entry, so the press lands on no tile → it pans.
  const cx = Math.round(zoomed.cx);
  const cy = Math.round(zoomed.cy);
  await gw.panFocusedGrid(cx, cy, cx - 1, cy - 1);
  const panned = await gw.focused();
  const moved = Math.abs(panned.cx - zoomed.cx) + Math.abs(panned.cy - zoomed.cy);
  expect(moved, 'drag-pan shifted the viewport center').toBeGreaterThan(0.1);
});

test('right-drag splits the focused pane into two', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  expect((await gw.panes()).length, 'starts with one pane').toBe(1);

  await gw.splitFocusedPaneVertical();

  expect((await gw.panes()).length, 'split produced a second pane').toBe(2);
});

test('right-drag and left-drag on the divider resize the panes', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // Right-drag the divider left → the left pane shrinks.
  const r = await gw.resizeDivider('right', -150);
  expect(r.after, 'right-drag shrank the left pane').toBeLessThan(r.before);

  // Left-drag the divider right → the left pane grows again (clamped resize).
  const l = await gw.resizeDivider('left', 150);
  expect(l.after, 'left-drag grew the left pane').toBeGreaterThan(l.before);
});

test('right-click on the corner circle ascends out of a descended well', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const root = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  expect((await gw.focused()).gridID, 'descended').not.toBe(root);

  await gw.rightClickPlus();
  expect((await gw.focused()).gridID, 'corner right-click ascended to root').toBe(root);
});

test('middle-click ascends out of a descended well', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const root = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  // Create a well and descend into it.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();
  expect(inside.gridID, 'descended into the well child grid').not.toBe(root);

  // Middle-click ascends back to the parent grid (the universal ascend).
  await gw.middleClickCell(cx, cy);
  const out = await gw.focused();
  expect(out.gridID, 'middle-click returned to the root grid').toBe(root);
});
