import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Nesting: a pane tile inside a pane of another pane tile. The stack grows, the
// one bar shows breadcrumbs, and, the load-bearing assertion, the outer
// workspace's persisted layout records its pane's place as B's containing grid,
// never as inside B. Workspace membership is session-only, like portal frames,
// because persisting it would make descending into B a write to A, and reading
// never mutates. It also shows self-reference is safe by construction:
// descending into A from inside A neither hangs nor recurses, since a restore
// never auto-descends.

async function workspaceState(window: any): Promise<{ depth: number; names: string[]; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function barClickCrumb(gw: any, level: number): Promise<void> {
  // One chain: leaving workspace `level` and everything deeper means going to
  // level-1, the crumb before that boundary.
  await gw.leaveWorkspace(level - 1);
}

test('nested workspaces: breadcrumbs, session-only membership, safe self-reference', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  // Two pane tiles side by side in the root grid: A and its neighbor B.
  await gw.openPalette();
  await gw.dragCreate('pane', ax, ay);
  await gw.openPalette();
  await gw.dragCreate('pane', ax + 2, ay);
  const snap = await gw.getGrid(rootGrid);
  const A = tileAt(snap, 'pane', ax, ay);
  const B = tileAt(snap, 'pane', ax + 2, ay);
  expect(A && B, 'both pane tiles persisted').toBeTruthy();

  // Enter A: the capture puts its pane on the containing grid, where B sits, so
  // no navigation is needed.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);

  // A's persister records the pane's place: the grid containing B.
  await expect.poll(async () => {
    try {
      return (await gw.getTileContent(A!.id)).includes(`"anchor":"${rootGrid}"`);
    } catch {
      return false;
    }
  }, { message: "A's blob must record the leaf's place (B's containing grid)", timeout: 10_000 }).toBe(true);

  // Descend into B: depth 2, two crumbs, and A's blob still records the
  // containing place. Being inside B never reaches the blob, and there is no
  // field for it, so the assertion is that the whole layout stays a place rather
  // than a membership.
  await gw.descendCell(ax + 2, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(2);
  expect((await workspaceState(window)).names.length).toBe(2);
  expect((await workspaceState(window)).tileID).toBe(B!.id);
  const aBlob = await gw.getTileContent(A!.id);
  expect(aBlob, "A's persisted layout must name B's containing grid, not B").toContain(`"anchor":"${rootGrid}"`);

  // Leave everything with one click on the outermost crumb: leaving A leaves B
  // too, and lands back on the session tree.
  await barClickCrumb(gw, 1);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);

  // Self-reference: A's pane still shows the grid containing A itself. Descending
  // into A from inside A pushes a second frame and lands on the stored layout,
  // with no hang and no recursion.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);
  await gw.descendCell(ax, ay); // into A, from inside A
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(2);
  expect((await workspaceState(window)).tileID).toBe(A!.id);
  await barClickCrumb(gw, 1);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
});
