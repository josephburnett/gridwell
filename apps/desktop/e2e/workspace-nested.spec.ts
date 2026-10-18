import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A workspace tile inside a pane of another workspace tile. The stack grows, the bar shows
// breadcrumbs, and the outer workspace's persisted layout records its pane's
// place as B's containing grid rather than as inside B. Workspace membership is
// session-only, like portal frames, because persisting it would make descending
// into B a write to A, and reading never mutates. Self-reference is safe for the
// same reason: descending into A from inside A neither hangs nor recurses, since
// a restore never auto-descends.

async function workspaceState(window: any): Promise<{ depth: number; names: string[]; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

// barClickCrumb leaves workspace `level` and everything deeper, which means
// landing at level-1.
async function barClickCrumb(gw: any, level: number): Promise<void> {
  await gw.leaveWorkspace(level - 1);
}

test('nested workspaces: breadcrumbs, session-only membership, safe self-reference', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  // Two workspace tiles side by side in the root grid, A and B.
  await gw.openMenu();
  await gw.dragCreate('workspace', ax, ay);
  await gw.openMenu();
  await gw.dragCreate('workspace', ax + 2, ay);
  const snap = await gw.getGrid(rootGrid);
  const A = tileAt(snap, 'workspace', ax, ay);
  const B = tileAt(snap, 'workspace', ax + 2, ay);
  expect(A && B, 'both workspace tiles persisted').toBeTruthy();

  // The capture puts A's pane on the containing grid, where B sits, so no
  // navigation is needed.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);

  // A's persister records the pane's place, which is the grid containing B.
  await expect.poll(async () => {
    try {
      return (await gw.getTileContent(A!.id)).includes(`"anchor":"${rootGrid}"`);
    } catch {
      return false;
    }
  }, { message: "A's blob must record the leaf's place (B's containing grid)", timeout: 10_000 }).toBe(true);

  // Being inside B never reaches A's blob, and there is no field for it, so A's
  // layout stays a place.
  await gw.descendCell(ax + 2, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(2);
  expect((await workspaceState(window)).names.length).toBe(2);
  expect((await workspaceState(window)).tileID).toBe(B!.id);
  const aBlob = await gw.getTileContent(A!.id);
  expect(aBlob, "A's persisted layout must name B's containing grid, not B").toContain(`"anchor":"${rootGrid}"`);

  // One click on the outermost crumb leaves A, which leaves B too.
  await barClickCrumb(gw, 1);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);

  // A's pane still shows the grid containing A itself, so descending into A from
  // inside A pushes a second frame and lands on the stored layout.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);
  await gw.descendCell(ax, ay); // into A, from inside A
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(2);
  expect((await workspaceState(window)).tileID).toBe(A!.id);
  await barClickCrumb(gw, 1);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
});
