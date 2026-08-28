import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Reload inside a workspace: the URL is the pane tile (`?w=` — the workspace
// IS the place; its interior is server-owned), so a fresh boot restores the
// arrangement from the layout blob. The workspace STACK is session-only by
// design (like portal frames), so after the reload the bar's ascent falls
// back to the pane tile's containing grid rather than a remembered outer
// tree.

async function workspaceState(window: any): Promise<{ depth: number; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

test('reload restores the workspace from ?w=; post-reload bar ascent lands at the containing grid', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt).toBeTruthy();

  // Enter and arrange: a split the reload must bring back.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await gw.splitFocusedPaneVertical();
  await expect.poll(async () => {
    try {
      return (await gw.getTileContent(pt!.id)).includes('"split"');
    } catch {
      return false;
    }
  }, { message: 'persister must write the split before the reload', timeout: 10_000 }).toBe(true);

  // The URL now names the workspace as the place.
  expect(window.url()).toContain('w=');

  // Fresh boot straight into the workspace (?w= + the e2e gate the harness
  // window normally carries).
  await window.evaluate(
    ([tileId]) => {
      location.href = `${location.origin}/?w=${encodeURIComponent(tileId)}&e2e=1`;
    },
    [pt!.id],
  );
  await window.waitForFunction(() => (window as any).__gridwellTest !== undefined);
  await expect.poll(async () => (await workspaceState(window)).depth, {
    message: 'boot must restore the workspace from ?w=',
    timeout: 15_000,
  }).toBe(1);
  expect((await workspaceState(window)).tileID).toBe(pt!.id);
  await expect.poll(async () => (await gw.panes()).length, {
    message: 'the split arrangement must come back from the blob',
  }).toBe(2);

  // Post-reload the stack has no outer tree: the bar falls back to the pane
  // tile's containing grid.
  // The bar lives inside the FOCUSED pane (issue #220); leaving is the
  // crumb BEFORE the pane boundary (one-chain nav, #245: click = go there).
  await gw.leaveWorkspace();
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  // The re-anchor fetches the tile asynchronously (a click handler cannot
  // block on the network), so the landing settles within a beat.
  await expect.poll(async () => (await gw.focused()).gridID, {
    message: 'fallback ascent must land at the containing grid',
  }).toBe(rootGrid);
});
