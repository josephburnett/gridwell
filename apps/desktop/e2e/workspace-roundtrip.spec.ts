import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The workspace round trip — the pane-tile face of "things stay as you left
// them", asserted at the seam where it can actually break (descend → arrange
// → ascend → re-descend, crossing client tree ⇄ layout blob both ways):
//   1. descending into a pane tile swaps the whole tree and raises the bar;
//   2. arranging inside (split + navigate) persists to the blob without any
//      explicit save gesture (the snapshot-diff persister);
//   3. ascending via the bar restores the OUTER pane arrangement exactly;
//   4. re-descending restores the INNER arrangement exactly.

// stablePane projects a thPanes entry down to the fields that must survive
// the workspace round trip untouched.
function stablePane(p: Record<string, unknown>) {
  const { id, x, y, w, h, anchor, path, gridID, textFocus, cx, cy, zoom } = p as any;
  return { id, x, y, w, h, anchor, path, gridID, textFocus, cx, cy, zoom };
}

async function workspaceState(window: any): Promise<{ depth: number; names: string[]; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

// barClick left-clicks the workspace bar's leftmost crumb. The bar band sits
// directly below the lowest pane edge (rootLayoutRect reserves it), so its
// vertical center is that edge + half the row height.
async function barClick(gw: any, window: any): Promise<void> {
  const panes = await gw.panes();
  const barTop = Math.max(...panes.map((p: any) => p.y + p.h));
  await window.mouse.click(30, barTop + 13, { button: 'right' });
  await gw.waitIdle();
}

test('workspace round trip: outer panes byte-identical, inner layout restored', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt, 'pane tile persisted').toBeTruthy();

  // Snapshot the outer arrangement (single pane descended into localdb).
  const outerBefore = (await gw.panes()).map(stablePane);
  expect(await workspaceState(window)).toMatchObject({ depth: 0 });

  // 1. Descend: the tree swaps, the bar appears.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth, {
    message: 'descending into the pane tile must enter the workspace',
  }).toBe(1);
  expect((await workspaceState(window)).tileID).toBe(pt!.id);

  // The organize-this default: a fresh workspace opens on the grid its
  // tile was dropped into — the place you were organizing — not home.
  expect((await gw.focused()).gridID, 'a fresh workspace must open on its containing grid').toBe(rootGrid);

  // 2. Arrange: split (both leaves inherit the containing grid).
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // The persister writes the arrangement without any save gesture: the
  // blob appears on the server (never-arranged tiles have no content).
  await expect.poll(async () => {
    try {
      const body = await gw.getTileContent(pt!.id);
      return body.includes('"split"') ? 'split-persisted' : body.slice(0, 40);
    } catch {
      return '';
    }
  }, { message: 'the debounced persister must write the split layout', timeout: 10_000 }).toBe('split-persisted');

  // 3. Ascend via the bar: back to the session tree, byte-identical.
  await barClick(gw, window);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const outerAfter = (await gw.panes()).map(stablePane);
  expect(outerAfter, 'outer arrangement must be exactly as it was left').toEqual(outerBefore);

  // 4. Re-descend: the inner arrangement is exactly as it was left — two
  // panes, one of them inside the localdb plugin.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const inner = await gw.panes();
  const resolved = inner.filter((p: any) => p.gridID === rootGrid);
  expect(resolved.length, 'both leaves must still frame the containing grid').toBe(2);

  // Teardown: leave the workspace so the shared session ends at the root.
  await barClick(gw, window);
});
