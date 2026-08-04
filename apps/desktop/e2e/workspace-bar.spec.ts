import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The bottom bar is ALWAYS-reserved layout (issue #212) carrying the ONE
// nav chain (issue #245): the complete path from the root — outer chain,
// pane-tile boundary crumb, inner chain — and the band never overlays
// pane content. Every crumb click GOES THERE: the current boundary is a
// no-op (you are there), and leaving is clicking any crumb before it.
// The workspace boundary still belongs to the bar alone:
//   - the IN-PANE ascent gesture (middle click) on a fully-ascended pane
//     does NOT leave the workspace — the two ascent vocabularies never blur;
//   - RIGHT-click on the boundary crumb renames the workspace inline.

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

  // The band lives INSIDE the focused pane's border (issues #220/#223):
  // its top edge is one row + the border above the pane's bottom, so the
  // border wraps all the way around. Outside a workspace there is no
  // workspace crumb and no anchor block (reversal 2026-07-31) — the chain
  // starts the band. On a grid the theme is the blue grid family.
  const outside = await bar(window);
  expect((await workspaceState(window)).depth).toBe(0);
  const fp0 = await gw.focused();
  const borderPx = fp0.y + fp0.h - outside.height - outside.top;
  expect(borderPx, 'the band sits inside the pane border').toBeGreaterThan(0);
  expect(borderPx, 'by exactly the border width').toBeLessThan(8);
  expect(outside.segments.some((s: any) => s.kind === 'pane'), 'no boundary crumb outside').toBe(false);
  expect(outside.segments[0].kind, 'the chain starts the band').toBe('chain');
  expect(outside.band, 'grid-family band shade').toBe('#151b2e');
  expect(outside.button, 'grid-family button hue').toBe('#4a6fff');
  expect(fp0.x, 'no outline gutter outside a workspace').toBe(0);
  expect(fp0.y, 'no outline gutter outside a workspace').toBe(0);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt).toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  // The teal workspace outline is reserved layout (issue #228): panes inset
  // by its width on every side, so the line never paints over the panes'
  // own kind-colored borders — the strip's reserved-band pattern again.
  const winW = await window.evaluate(() => window.innerWidth);
  const winH = await window.evaluate(() => window.innerHeight);
  const wp = (await gw.panes())[0];
  expect(wp.x, 'left outline gutter exists').toBeGreaterThan(0);
  expect(wp.x, 'the gutter is thin').toBeLessThan(8);
  expect(wp.y, 'top gutter matches').toBe(wp.x);
  expect(winW - (wp.x + wp.w), 'right gutter matches').toBe(wp.x);
  expect(winH - (wp.y + wp.h), 'bottom gutter matches').toBe(wp.x);

  // Inside: ONE nav chain (issue #245) — the outer chain, then the pane
  // tile's boundary crumb, then the inner chain. Complete path from root.
  const inside = await bar(window);
  const crumb = inside.segments.find((s: any) => s.kind === 'pane' && s.level === 1);
  expect(crumb, 'the pane-tile boundary crumb must appear').toBeTruthy();
  const boundaryIdx = inside.segments.indexOf(crumb);
  expect(boundaryIdx, 'the outer chain precedes the boundary').toBeGreaterThan(0);
  expect(inside.segments[boundaryIdx - 1].kind).toBe('chain');
  expect(inside.segments.length, 'the inner chain follows the boundary').toBeGreaterThan(boundaryIdx + 1);

  // Clicking the CURRENT boundary crumb GOES THERE — which is here: a
  // no-op, never a close (#245: click = go there; the last view closes by
  // clicking any crumb before its boundary).
  await window.mouse.click(crumb.x + crumb.w / 2, inside.top + inside.height / 2);
  await gw.waitIdle();
  expect((await workspaceState(window)).depth, 'clicking the current boundary stays put').toBe(1);

  // The workspace's default pane frames the containing grid with nothing
  // to pop in-pane (no path, no portal frames). Middle-click (the universal
  // in-pane ascend) must NOT leave the workspace — the bar is the only exit.
  const inner = await gw.focused();
  await window.mouse.click(inner.x + inner.w / 2, inner.y + inner.h / 2, { button: 'middle' });
  await gw.waitIdle();
  expect((await workspaceState(window)).depth, 'in-pane ascent crossed the workspace boundary').toBe(1);

  // RIGHT-click the crumb: the shared inline rename input, aimed at the
  // workspace. Enter commits a user-owned name; the crumb and the tile's
  // alt both update.
  await window.mouse.click(crumb.x + 20, inside.top + inside.height / 2, { button: 'right' });
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

  // Leaving = clicking the crumb BEFORE the boundary (go to the outer
  // place); the band stays.
  await gw.leaveWorkspace();
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);

  // Wheel over the band zooms the CURRENT pane, centered (issue #220) —
  // the escape hatch for well-tiled grids.
  const zBefore = (await gw.focused()).zoom;
  const b3 = await bar(window);
  await window.mouse.move(b3.left + b3.width / 2, b3.top + b3.height / 2);
  await window.mouse.wheel(0, -120);
  await expect.poll(async () => (await gw.focused()).zoom).not.toBeCloseTo(zBefore, 5);
});
