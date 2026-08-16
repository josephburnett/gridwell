import { test, expect } from './fixtures';

// Proves the god-object consolidation's lifecycle end of things: when a pane is
// collapsed, forgetPane tears down its per-pane state (a.locals entry) rather
// than leaking it. Also the first e2e to exercise the pane-collapse gesture at
// all (previously zero coverage of the collapse path).
test('collapsing a pane tears down its per-pane state (forgetPane)', async ({ gw }) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Give the focused pane per-pane state: create a well, descend, ascend. The
  // descent pushes the parent viewport onto the pane's ascent stack, which
  // materializes the pane's entry in a.locals.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.middleClickCell(cx, cy); // ascend back out
  const origId = (await gw.focused()).id;
  expect(await gw.localPaneIds(), 'the pane has per-pane state before the split').toContain(origId);

  // Split (the original keeps its id + state on the LEFT), then collapse it.
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length, 'split produced two panes').toBe(2);
  await gw.collapseLeftPane();

  // The left pane is gone, and its per-pane state went with it — not orphaned.
  expect((await gw.panes()).length, 'collapsed back to one pane').toBe(1);
  expect(await gw.localPaneIds(), "the collapsed pane's per-pane state was torn down").not.toContain(origId);
});
