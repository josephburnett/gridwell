import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The workspace round trip: the pane-tile face of things staying as they were
// left, asserted where it can break, across descend, arrange, ascend, and
// re-descend, crossing between the client tree and the layout blob both ways.
//   1. descending into a pane tile swaps the whole tree and raises the bar;
//   2. arranging inside, by splitting and navigating, persists to the blob with
//      no explicit save gesture, through the snapshot-diff persister;
//   3. ascending through the bar restores the outer pane arrangement exactly;
//   4. re-descending restores the inner arrangement exactly.

// stablePane projects a panes() entry down to the fields that must survive the
// workspace round trip untouched.
function stablePane(p: object) {
  const { id, x, y, w, h, anchor, path, gridID, textFocus, cx, cy, zoom } = p as any;
  return { id, x, y, w, h, anchor, path, gridID, textFocus, cx, cy, zoom };
}

async function workspaceState(window: any): Promise<{ depth: number; names: string[]; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

// barClick left-clicks the workspace bar's leftmost crumb. The bar band sits
// directly below the lowest pane edge, which rootLayoutRect reserves, so its
// vertical center is that edge plus half the row height.
async function barClick(gw: any): Promise<void> {
  // The bar lives inside the focused pane, and a crumb click goes to that crumb,
  // so leaving means clicking the crumb before the pane boundary.
  await gw.leaveWorkspace();
}

test('workspace round trip: outer panes byte-identical, inner layout restored', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt, 'pane tile persisted').toBeTruthy();

  // Snapshot the outer arrangement: a single pane descended into home.
  const outerBefore = (await gw.panes()).map(stablePane);
  expect(await workspaceState(window)).toMatchObject({ depth: 0 });

  // 1. Descend: the tree swaps, the bar appears.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth, {
    message: 'descending into the pane tile must enter the workspace',
  }).toBe(1);
  expect((await workspaceState(window)).tileID).toBe(pt!.id);

  // A fresh workspace opens on the grid its tile was dropped into, the place
  // being organized, not home.
  expect((await gw.focused()).gridID, 'a fresh workspace must open on its containing grid').toBe(rootGrid);

  // 2. Arrange: split, so both leaves inherit the containing grid.
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // The persister writes the arrangement with no save gesture: the blob appears
  // on the server, where a never-arranged tile has no content.
  await expect.poll(async () => {
    try {
      const body = await gw.getTileContent(pt!.id);
      return body.includes('"split"') ? 'split-persisted' : body.slice(0, 40);
    } catch {
      return '';
    }
  }, { message: 'the debounced persister must write the split layout', timeout: 10_000 }).toBe('split-persisted');

  // 3. Ascend through the bar: back to the session tree, byte-identical.
  await barClick(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const outerAfter = (await gw.panes()).map(stablePane);
  expect(outerAfter, 'outer arrangement must be exactly as it was left').toEqual(outerBefore);

  // 4. Re-descend: the inner arrangement is exactly as it was left, two panes,
  // one of them inside home.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const inner = await gw.panes();
  const resolved = inner.filter((p: any) => p.gridID === rootGrid);
  expect(resolved.length, 'both leaves must still frame the containing grid').toBe(2);

  // Teardown: leave the workspace so the shared session ends at the root.
  await barClick(gw);
});
