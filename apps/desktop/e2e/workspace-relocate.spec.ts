import { test, expect } from './fixtures';
import { tileAt, placeTile } from './oracle';

// A workspace leaf references its tile by anchor, path, and id, and the path goes
// stale the moment the tile is moved: ids are immutable, paths are not.
// Re-entering the workspace must heal the leaf. The client notices the stored
// path no longer leads to the tile's grid, asks the server's LocateTile for the
// current containing-well chain, and rebinds the pane there, so the descent works
// and the crumbs show a true path. The move happens through the server directly,
// as a foreign writer, which is the out-from-under case a live session never
// sees.

test('re-entering a workspace heals a leaf whose tile was moved into a well', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // A markdown tile with real content, persisted.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(doc, 'markdown created').toBeTruthy();
  await gw.descendCell(cx, cy);
  await gw.typeText('relocate-me');
  await gw.ascendViaCrumb();

  // A well to move it into, and a workspace tile.
  await gw.openPalette();
  await gw.dragCreate('well', cx + 2, cy);
  const well = tileAt(await gw.getGrid(grid), 'well', cx + 2, cy)!;
  await gw.openPalette();
  await gw.dragCreate('pane', cx - 2, cy);
  // Wait until the pane tile is rendered before clicking it. The create's
  // optimistic commit and the background refetch land on their own schedule, and
  // a click into the gap lands on empty grid with no workspace opening.
  const pt = tileAt(await gw.getGrid(grid), 'pane', cx - 2, cy)!;
  await expect
    .poll(async () => (await gw.focused()).tileIds, { timeout: 10_000 })
    .toContain(pt.id);

  // Enter the workspace and bind its pane to the markdown tile.
  await gw.descendCell(cx - 2, cy);
  await expect
    .poll(async () => window.evaluate(() => (window as any).__gridwellTest.workspace().depth))
    .toBe(1);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe(doc.id);

  // Leave the workspace; the crumb click flushes the layout with the leaf.
  await gw.leaveWorkspace();
  await expect
    .poll(async () => window.evaluate(() => (window as any).__gridwellTest.workspace().depth))
    .toBe(0);

  // A foreign writer moves the tile into the well.
  const fresh = (await gw.getGrid(grid)).tiles!.find((t: any) => t.id === doc.id)!;
  await placeTile(gw.origin, doc.id, Number(fresh.version), well.childGridId!, 0, 0, 1, 1);
  await expect
    .poll(async () => ((await gw.getGrid(well.childGridId!)).tiles ?? []).some((t: any) => t.id === doc.id))
    .toBe(true);

  // Re-enter: the leaf must find the moved tile and bind to it in its new grid.
  // Without the heal the pane restores at the stale path, with the content frame
  // set but the pane's grid still the old root: a dead preview.
  await gw.descendCell(cx - 2, cy);
  await expect
    .poll(async () => window.evaluate(() => (window as any).__gridwellTest.workspace().depth))
    .toBe(1);
  await expect
    .poll(async () => {
      const p = await gw.focused();
      return { focus: p.textFocus, grid: p.gridID };
    }, { timeout: 15_000 })
    .toEqual({ focus: doc.id, grid: well.childGridId });

  // The healed descent is real: the crumb chain now passes through the well, so
  // root, well, and tile make three chain crumbs.
  const healed = await window.evaluate(() => (window as any).__gridwellTest.bar());
  const chain = healed.segments.filter((s: any) => s.kind === 'chain');
  expect(chain.length, 'crumbs show the true path through the well').toBeGreaterThanOrEqual(3);
});
