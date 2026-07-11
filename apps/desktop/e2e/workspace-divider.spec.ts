import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #173: inside a pane-tile workspace, panes lay out into the
// bar-adjusted rootLayoutRect, but dividerOnSide built its divider geometry
// from the FULL window rect — the horizontal-divider midlines drifted from
// the real pane edges (by up to the bar height, growing downward), the
// half-pixel adjacency match never fired, and up/down resize of a stacked
// boundary never armed — for right- AND left-drag alike (they share
// dividerOnSide). At depth 0 wsbar.Height is 0 and the rects coincide, so
// only a workspace shows it. Worse than a no-op: with no divider found, the
// right-drag classifies as an edge SPLIT instead. The fix makes
// dividerOnSide read the one layout-rect owner (rootLayoutRect); this spec
// crosses the real gesture seam inside a workspace.

async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

// barClick right-clicks the workspace bar's crumb to ascend (the bar band
// sits directly below the lowest pane edge).
async function barClick(gw: any, window: any): Promise<void> {
  const panes = await gw.panes();
  const barTop = Math.max(...panes.map((p: any) => p.y + p.h));
  await window.mouse.click(30, barTop + 13, { button: 'right' });
  await gw.waitIdle();
}

test('stacked-pane divider resizes inside a workspace (both buttons)', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt, 'pane tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  // Stack two panes; the boundary between them is a horizontal divider.
  await gw.splitFocusedPaneHorizontal();
  expect((await gw.panes()).length).toBe(2);

  // Right-drag the boundary down: the top pane must GROW — and the gesture
  // must be a RESIZE, not a misclassified edge split (pane count stays 2).
  const r = await gw.resizeHDivider('right', 60);
  expect((await gw.panes()).length, 'right-drag on the divider must resize, not split').toBe(2);
  expect(r.after, 'right-drag must move the stacked boundary').toBeGreaterThan(r.before + 30);

  // Left-drag it back up: same divider, other button, same dividerOnSide.
  const l = await gw.resizeHDivider('left', -60);
  expect((await gw.panes()).length, 'left-drag on the divider must resize, not split').toBe(2);
  expect(l.after, 'left-drag must move the stacked boundary').toBeLessThan(l.before - 30);

  // Leave the workspace so the shared session ends at the root.
  await barClick(gw, window);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
});
