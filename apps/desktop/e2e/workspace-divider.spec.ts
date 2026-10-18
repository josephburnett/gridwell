import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Inside a pane-tile workspace, panes lay out into rootLayoutRect, which insets
// by wsOutlinePx above depth 0, and dividerGrab has to read that same rect.
// Built from the full window instead, divider midlines drift from the real pane
// edges and pane.GrabDividers never fires, so a right-drag classifies as an
// edge split. Both buttons are driven here on a real divider.

async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

// Ascends out of the workspace; see leaveWorkspace in driver.ts.
async function barClick(gw: any): Promise<void> {
  await gw.leaveWorkspace();
}

test('stacked-pane divider left-resizes inside a workspace; right-drag splits', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt, 'pane tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  await gw.splitFocusedPaneHorizontal();
  expect((await gw.panes()).length).toBe(2);

  // The left button resizes, so the pane count stays 2.
  const r = await gw.resizeHDivider('left', 60);
  expect((await gw.panes()).length, 'left-drag on the divider must resize, not split').toBe(2);
  expect(r.after, 'left-drag must move the stacked boundary').toBeGreaterThan(r.before + 30);

  const l = await gw.resizeHDivider('left', -60);
  expect((await gw.panes()).length, 'left-drag on the divider must resize, not split').toBe(2);
  expect(l.after, 'left-drag must move the stacked boundary').toBeLessThan(l.before - 30);

  // The drag pulls away from the border, into the top pane, so the new pane is
  // drawn out of the edge; dragging toward the border would cancel.
  await gw.resizeHDivider('right', -80);
  expect((await gw.panes()).length, 'border right-drag split a pane').toBe(3);

  await barClick(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
});
