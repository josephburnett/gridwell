import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Nesting: a pane tile inside a pane of another pane tile. The stack grows,
// the ONE bar shows breadcrumbs, and — the load-bearing assertion — the
// OUTER workspace's persisted layout records its pane's place as B's
// CONTAINING grid, never "inside B": workspace membership is session-only
// (like portal frames), because persisting it would make descent-into-B a
// WRITE to A ("reading never mutates"). Also proves self-reference is safe
// by construction: descending into A from inside A neither hangs nor
// recurses (restore never auto-descends).

async function workspaceState(window: any): Promise<{ depth: number; names: string[]; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function barClickCrumb(gw: any, window: any, level: number): Promise<void> {
  const panes = await gw.panes();
  const barTop = Math.max(...panes.map((p: any) => p.y + p.h));
  const width = Math.max(...panes.map((p: any) => p.x + p.w));
  const state = await workspaceState(window);
  // Crumbs divide the width evenly (capped at 240px) — click crumb k's center.
  const crumbW = Math.min(width / Math.max(state.depth, 1), 240);
  await window.mouse.click((level - 1) * crumbW + crumbW / 2, barTop + 13, { button: 'right' });
  await gw.waitIdle();
}

test('nested workspaces: breadcrumbs, session-only membership, safe self-reference', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  // Two pane tiles side by side in the plugin root: A and (its neighbor) B.
  await gw.openPalette();
  await gw.dragCreate('pane', ax, ay);
  await gw.openPalette();
  await gw.dragCreate('pane', ax + 2, ay);
  const snap = await gw.getGrid(rootGrid);
  const A = tileAt(snap, 'pane', ax, ay);
  const B = tileAt(snap, 'pane', ax + 2, ay);
  expect(A && B, 'both pane tiles persisted').toBeTruthy();

  // Enter A: the organize-this default puts its pane on the CONTAINING
  // grid (where B sits) — no navigation needed.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);

  // A's persister records the pane's PLACE — the grid containing B.
  await expect.poll(async () => {
    try {
      return (await gw.getTileContent(A!.id)).includes(`"anchor":"${rootGrid}"`);
    } catch {
      return false;
    }
  }, { message: "A's blob must record the leaf's place (B's containing grid)", timeout: 10_000 }).toBe(true);

  // Descend into B: depth 2, two crumbs, and A's blob still records the
  // CONTAINING place — being-inside-B never reaches the blob (there is no
  // field for it; assert the whole layout stays a place, not a membership).
  await gw.descendCell(ax + 2, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(2);
  expect((await workspaceState(window)).names.length).toBe(2);
  expect((await workspaceState(window)).tileID).toBe(B!.id);
  const aBlob = await gw.getTileContent(A!.id);
  expect(aBlob, "A's persisted layout must name B's containing grid, not B").toContain(`"anchor":"${rootGrid}"`);

  // Leave everything with one click on the OUTERMOST crumb (leave A ⇒
  // leaves B too): back to the session tree.
  await barClickCrumb(gw, window, 1);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);

  // Self-reference: A's pane still shows the grid containing A itself.
  // Descending into A from inside A pushes a second frame and lands on the
  // stored layout — no hang, no auto-recursion.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);
  await gw.descendCell(ax, ay); // A, from inside A
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(2);
  expect((await workspaceState(window)).tileID).toBe(A!.id);
  await barClickCrumb(gw, window, 1);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
});
