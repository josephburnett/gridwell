import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The bar owns the workspace boundary — and nothing else does:
//   - no bar outside a workspace (the landing page / session tree reserves
//     no band; panes get the full height);
//   - inside a workspace, the IN-PANE ascent gesture (middle click) on a
//     fully-ascended pane does NOT leave the workspace — the two ascent
//     vocabularies never blur (a pane with nothing to pop just stays);
//   - clicking the crumb leaves.

async function workspaceState(window: any): Promise<{ depth: number; names: string[] }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function panesBottom(gw: any): Promise<number> {
  const panes = await gw.panes();
  return Math.max(...panes.map((p: any) => p.y + p.h));
}

test('the bar exists only inside a workspace, and in-pane ascent never crosses it', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  // Outside: no reserved band (panes reach the strip-less bottom).
  const bottomOutside = await panesBottom(gw);
  expect((await workspaceState(window)).depth).toBe(0);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt).toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  // Inside: the bar band is reserved — panes end a row higher.
  const bottomInside = await panesBottom(gw);
  expect(bottomInside, 'the bar must reserve layout, not overlay panes').toBeLessThan(bottomOutside);

  // The workspace's default pane sits at home: nothing to pop in-pane.
  // Middle-click (the universal in-pane ascend) must NOT leave the
  // workspace — the bar is the only exit.
  const inner = await gw.focused();
  await window.mouse.click(inner.x + inner.w / 2, inner.y + inner.h / 2, { button: 'middle' });
  await gw.waitIdle();
  expect((await workspaceState(window)).depth, 'in-pane ascent crossed the workspace boundary').toBe(1);

  // The crumb leaves.
  await window.mouse.click(30, bottomInside + 13);
  await gw.waitIdle();
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  expect(await panesBottom(gw)).toBe(bottomOutside);
});
