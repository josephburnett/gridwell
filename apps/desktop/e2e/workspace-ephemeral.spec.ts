import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A pane-tile workspace owns its ephemeral leaves. An ephemeral shell opened
// inside the workspace survives ascent, since the boundary detaches and never
// kills, reattaches live on re-descent, so the workspace is a setup that stays
// as it was left, and dies exactly when the pane tile is deleted: the
// arrangement is a view, and deleting it terminates what it owns, as closing a
// pane does.

async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function barAscend(gw: any): Promise<void> {
  // The bar lives inside the focused pane, and a crumb click goes to that crumb,
  // so leaving means clicking the crumb before the pane boundary.
  await gw.leaveWorkspace();
}

test('workspace ephemeral shell: survives ascent, reattaches on descent, dies with the pane tile', async ({
  gw,
  window,
}) => {
  const scratchGridID = (await gw.plugins()).find((l) => l.kind === 'home')!.scratchGridID;
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;
  expect(pt, 'pane tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  // An ephemeral shell inside the workspace, live.
  await gw.clickPaletteSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellTile = (await gw.focused()).textFocus;
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      timeout: 15_000,
    })
    .toBe('webgl');

  // Let the persister record the arrangement: the blob is the ownership record
  // the sweep and the reap read.
  await expect
    .poll(async () => {
      try {
        return (await gw.getTileContent(pt.id)).includes(shellTile) ? 'recorded' : '';
      } catch {
        return '';
      }
    }, { timeout: 10_000 })
    .toBe('recorded');

  // Ascend out of the workspace: the ephemeral survives, detached rather than
  // killed.
  await barAscend(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const scratch = await gw.getGrid(scratchGridID);
  const tileIds = (scratch.tiles ?? []).map((t: any) => String(t.id));
  expect(
    tileIds.some((id: string) => id === shellTile || shellTile.endsWith('/' + id)),
    `the ephemeral shell tile survived the workspace ascent (scratch=${JSON.stringify(tileIds)}, shell=${shellTile})`,
  ).toBe(true);

  // Re-descend: the shell leaf comes back live with no gesture, because the
  // workspace reattaches what it owns. Without that it shows a frozen preview
  // and the renderer stays empty.
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).toBe(
    shellTile,
  );
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      message: 'the ephemeral shell must reattach live on workspace descent',
      timeout: 15_000,
    })
    .toBe('webgl');

  // Deleting the pane tile parks it in the trash, and a trashed workspace keeps
  // its ephemerals, so a restore comes back whole.
  await barAscend(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  await gw.deleteTileCell(wx, wy);
  await gw.clickPluginSwatch('trash');
  const troot = await gw.focused();
  const month = (await gw.getGrid(troot.gridID)).tiles!.find((t) => t.kind === 'well')!;
  await gw.descendCell(Number(month.x ?? 0), Number(month.y ?? 0));
  const monthGridID = (await gw.focused()).gridID;
  let parked: import('./oracle').Tile | undefined;
  await expect
    .poll(async () => {
      parked = ((await gw.getGrid(monthGridID)).tiles ?? []).find((t) => t.kind === 'pane');
      return !!parked;
    }, { message: 'the pane tile is parked under the month well', timeout: 10_000 })
    .toBe(true);
  expect(
    ((await gw.getGrid(scratchGridID)).tiles ?? []).length,
    'a trashed workspace keeps its ephemeral shell',
  ).toBe(1);

  // Destroying it in the trash terminates what it owns: the scratch tile, and
  // with it the tmux session, which also leaves teardown clean.
  await gw.deleteTileCell(Number(parked!.x ?? 0), Number(parked!.y ?? 0));
  await expect
    .poll(async () => {
      const g = await gw.getGrid(scratchGridID);
      return (g.tiles ?? []).length;
    }, { message: 'destroying the pane tile must reap its ephemeral shell', timeout: 10_000 })
    .toBe(0);
});
