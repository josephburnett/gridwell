import { test, expect } from './fixtures';
import { tileAt, trashed } from './oracle';

// A workspace tile workspace owns its ephemeral leaves. An ephemeral shell opened
// inside the workspace survives ascent, because the boundary detaches and never
// kills, and it reattaches live on re-descent, so the arrangement stays as it
// was left. When the workspace tile is destroyed it is closed into the trash,
// the way closing a pane closes what it shows.

async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function barAscend(gw: any): Promise<void> {
  await gw.leaveWorkspace();
}

test('workspace ephemeral shell: survives ascent, reattaches on descent, goes to the trash with the workspace tile', async ({
  gw,
  window,
}) => {
  await gw.enterSource('home');
  const scratchGridID = await gw.scratchGridID('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'workspace', wx, wy)!;
  expect(pt, 'workspace tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  await gw.clickMenuSwatch('shell');
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

  await barAscend(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const scratch = await gw.getGrid(scratchGridID);
  const tileIds = (scratch.tiles ?? []).map((t: any) => String(t.id));
  expect(
    tileIds.some((id: string) => id === shellTile || shellTile.endsWith('/' + id)),
    `the ephemeral shell tile survived the workspace ascent (scratch=${JSON.stringify(tileIds)}, shell=${shellTile})`,
  ).toBe(true);

  // The shell leaf comes back live with no gesture, because the workspace
  // reattaches what it owns. Without that it shows a frozen preview and the
  // renderer stays empty.
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

  // Deleting the workspace tile parks it in the trash, and a trashed workspace keeps
  // its ephemerals, so a restore comes back whole.
  await barAscend(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  await gw.deleteTileCell(wx, wy);
  await gw.clickSwatch('trash (home)');
  const troot = await gw.focused();
  const month = (await gw.getGrid(troot.gridID)).tiles!.find((t) => t.kind === 'door')!;
  await gw.descendCell(Number(month.x ?? 0), Number(month.y ?? 0));
  const monthGridID = (await gw.focused()).gridID;
  let parked: import('./oracle').Tile | undefined;
  await expect
    .poll(async () => {
      parked = ((await gw.getGrid(monthGridID)).tiles ?? []).find((t) => t.kind === 'workspace');
      return !!parked;
    }, { message: 'the workspace tile is parked under the month door', timeout: 10_000 })
    .toBe(true);
  expect(
    ((await gw.getGrid(scratchGridID)).tiles ?? []).length,
    'a trashed workspace keeps its ephemeral shell',
  ).toBe(1);

  // Destroying it in the trash closes what it held: the shell visit lands
  // beside it in the trash.
  await gw.deleteTileCell(Number(parked!.x ?? 0), Number(parked!.y ?? 0));
  await expect
    .poll(async () => {
      const g = await gw.getGrid(scratchGridID);
      return (g.tiles ?? []).length;
    }, { message: 'destroying the workspace tile must close its ephemeral shell', timeout: 10_000 })
    .toBe(0);
  await expect
    .poll(async () => (await trashed(gw.origin, shellTile))?.gridId, { message: 'the shell visit is in the trash', timeout: 10_000 })
    .toBe(monthGridID);
});
