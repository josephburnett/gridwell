import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The bottom bar is ALWAYS-reserved layout (issue #212): outside a workspace
// it carries the focused pane's descent chain (no workspace crumb), inside it
// gains the workspace crumbs — and the band never overlays pane content.
// The workspace boundary itself still belongs to the bar alone:
//   - the IN-PANE ascent gesture (middle click) on a fully-ascended pane
//     does NOT leave the workspace — the two ascent vocabularies never blur;
//   - the workspace crumb mirrors the pane name bubble: LEFT-click renames
//     the workspace inline (user-owned), RIGHT-click leaves.

async function workspaceState(window: any): Promise<{ depth: number; names: string[] }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function bar(window: any): Promise<{ top: number; height: number; segments: any[] }> {
  return window.evaluate(() => (window as any).__gridwellTest.bar());
}

async function panesBottom(gw: any): Promise<number> {
  const panes = await gw.panes();
  return Math.max(...panes.map((p: any) => p.y + p.h));
}

test('the bar is always reserved; workspace crumbs appear only inside; in-pane ascent never crosses', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  // Outside a workspace: the band is still there — panes end exactly at its
  // top edge (reserved layout, not an overlay) — with no workspace crumb,
  // just the chain (root inclusive).
  const outside = await bar(window);
  expect((await workspaceState(window)).depth).toBe(0);
  expect(await panesBottom(gw), 'panes must end at the bar, always').toBe(outside.top);
  expect(outside.segments.some((s: any) => s.kind === 'workspace')).toBe(false);
  expect(outside.segments.some((s: any) => s.kind === 'chain'), 'the chain shows even at depth 0').toBe(true);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt).toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  // Inside: same band, same reservation, plus the workspace crumb.
  const inside = await bar(window);
  expect(inside.top).toBe(outside.top);
  expect(await panesBottom(gw)).toBe(inside.top);
  const crumb = inside.segments.find((s: any) => s.kind === 'workspace' && s.index === 1);
  expect(crumb, 'the workspace crumb must appear').toBeTruthy();

  // The workspace's default pane frames the containing grid with nothing
  // to pop in-pane (no path, no portal frames). Middle-click (the universal
  // in-pane ascend) must NOT leave the workspace — the bar is the only exit.
  const inner = await gw.focused();
  await window.mouse.click(inner.x + inner.w / 2, inner.y + inner.h / 2, { button: 'middle' });
  await gw.waitIdle();
  expect((await workspaceState(window)).depth, 'in-pane ascent crossed the workspace boundary').toBe(1);

  // LEFT-click the crumb: the shared inline rename input (the name-bubble
  // gesture, aimed at the workspace). Enter commits a user-owned name; the
  // crumb and the tile's alt both update.
  await window.mouse.click(crumb.x + 20, inside.top + inside.height / 2);
  await window.locator('#gw-rename-input').waitFor({ timeout: 5_000 });
  await window.fill('#gw-rename-input', 'ops board');
  await window.keyboard.press('Enter');
  await expect.poll(async () => (await workspaceState(window)).names?.[0], {
    message: 'the crumb must show the new name',
  }).toBe('ops board');
  await expect.poll(async () => {
    const snap = await gw.getGrid(rootGrid);
    return (snap.tiles ?? []).find((t: any) => t.id === pt!.id)?.altText ?? '';
  }, { message: 'the rename must persist as the tile alt' }).toBe('ops board');
  expect((await workspaceState(window)).depth, 'renaming must not ascend').toBe(1);

  // RIGHT-click the crumb leaves (the ascent gesture); the band stays.
  await window.mouse.click(crumb.x + 20, inside.top + inside.height / 2, { button: 'right' });
  await gw.waitIdle();
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  expect(await panesBottom(gw)).toBe(outside.top);
});
