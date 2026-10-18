import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The workspace tile round trip, crossing between the client tree and the layout blob
// both ways. Descending into a workspace tile swaps the whole tree. Arranging inside,
// by splitting and navigating, persists to the blob with no save gesture,
// through the snapshot-diff persister. Ascending through the bar restores the
// outer arrangement exactly, and re-descending restores the inner one.

// stablePane projects a panes() entry down to the fields that must survive the
// workspace round trip untouched.
function stablePane(p: object) {
  const { id, x, y, w, h, anchor, path, gridID, textFocus, cx, cy, zoom } = p as any;
  return { id, x, y, w, h, anchor, path, gridID, textFocus, cx, cy, zoom };
}

async function workspaceState(window: any): Promise<{ depth: number; names: string[]; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

// barClick ascends out of the workspace by clicking a bar crumb. See
// leaveWorkspace in driver.ts for which crumb and why.
async function barClick(gw: any): Promise<void> {
  await gw.leaveWorkspace();
}

test('workspace round trip: outer panes byte-identical, inner layout restored', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'workspace', wx, wy);
  expect(pt, 'workspace tile persisted').toBeTruthy();

  // The outer arrangement is a single pane descended into home.
  const outerBefore = (await gw.panes()).map(stablePane);
  expect(await workspaceState(window)).toMatchObject({ depth: 0 });

  // Descending swaps the tree.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth, {
    message: 'descending into the workspace tile must enter the workspace',
  }).toBe(1);
  expect((await workspaceState(window)).tileID).toBe(pt!.id);

  // A fresh workspace opens on the grid its tile was dropped into, which is the
  // place being organized.
  expect((await gw.focused()).gridID, 'a fresh workspace must open on its containing grid').toBe(rootGrid);

  // Splitting leaves both panes on the containing grid.
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // The persister writes the arrangement with no save gesture. A never-arranged
  // tile has no content, so the blob appearing is the write.
  await expect.poll(async () => {
    try {
      const body = await gw.getTileContent(pt!.id);
      return body.includes('"split"') ? 'split-persisted' : body.slice(0, 40);
    } catch {
      return '';
    }
  }, { message: 'the debounced persister must write the split layout', timeout: 10_000 }).toBe('split-persisted');

  // Ascending returns to the session tree byte-identical.
  await barClick(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const outerAfter = (await gw.panes()).map(stablePane);
  expect(outerAfter, 'outer arrangement must be exactly as it was left').toEqual(outerBefore);

  // Re-descending restores the inner arrangement: two panes, both on the
  // containing grid.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const inner = await gw.panes();
  const resolved = inner.filter((p: any) => p.gridID === rootGrid);
  expect(resolved.length, 'both leaves must still frame the containing grid').toBe(2);

  // Leave the workspace so the shared session ends at the root.
  await barClick(gw);
});
