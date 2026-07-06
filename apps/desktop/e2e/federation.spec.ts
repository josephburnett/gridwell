import { test, expect } from './fixtures';
import { tileAt, Tile } from './oracle';

// The federated space, end to end through the real stack:
//   - the landing page is the NODE GRID — a real, read-only, server-owned
//     grid whose tiles are dashed links, one per plugin;
//   - right-dragging a well across a plugin boundary creates a LINK in the
//     destination (the grid is shared, never copied), and deleting the link
//     only unlinks;
//   - right-dragging a plugin tile off the landing page into a grid mounts
//     that plugin as a link well — the gesture the launcher's tiles exist for.
//
// Why was this not caught before? The launcher was a client-side special case
// with no grid behind it (nothing to drag), and cross-plugin CloneTile had no
// implementation — both halves of "link any plugin or grid into my grids"
// were missing and neither had a spec.

test.use({ extraPlugins: [{ kind: 'localdb', name: 'second' }] });

test('the landing page is the node grid: a real grid of plugin link tiles', async ({ gw, window }) => {
  // The hook is empty until ListPlugins and the node-grid fetch land.
  await window.waitForFunction(() => (window as any).__gridwellTest.launcher().length >= 2, null, {
    timeout: 15_000,
  });
  const tiles = await gw.launcher();
  expect(tiles.length, 'both plugins appear on the landing page').toBe(2);

  // The focused pane is anchored at a REAL grid ("<node>/0"), not a synthetic
  // client-side page — the oracle can read it like any grid.
  const f = await gw.focused();
  expect(f.gridID, 'pane anchored at the node grid').toMatch(/\/0$/);
  const g = await gw.getGrid(f.gridID);
  const labels = (g.tiles ?? []).map((t: Tile) => t.altText).sort();
  expect(labels, 'tiles labeled by config name').toEqual(['e2e', 'second']);
  for (const t of g.tiles ?? []) {
    expect(t.reference, `plugin tile ${t.altText} is a link (dashed)`).toBe(true);
    expect(t.kind).toBe('well');
  }
});

test('right-drag links a well across plugins; deleting the link never touches the source', async ({ gw }) => {
  // Pane A: enter "e2e", create a well OFF-center (cy-1) so later corner/center
  // clicks never land on it.
  await gw.enterPlugin('e2e');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const src = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  expect(src, 'source well created').toBeTruthy();

  // Pane B: split (the new pane ascends to the node grid), enter "second".
  // Focus panes with CORNER clicks throughout: pane centers hold tiles (the
  // node grid's plugin tiles are centered; the well sits near pane A's
  // center), and a center click would descend instead of focusing.
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  const b = panes.find((p) => p.id !== a.id)!;
  await gw.clickScreen(b.x + 20, b.y + 20);
  await gw.enterPlugin('second');
  const bNow = (await gw.panes()).find((p) => p.id === b.id)!;
  const tx = Math.round(bNow.cx);
  const ty = Math.round(bNow.cy) - 1;

  // The link gesture: right-drag the well from pane A into pane B.
  const aNow = (await gw.panes()).find((p) => p.id === a.id)!;
  await gw.clickScreen(aNow.x + 20, aNow.y + 20);
  await gw.cloneDragAcrossPanes(a.id, cx, cy, b.id, tx, ty);

  const link = tileAt(await gw.getGrid(bNow.gridID), 'well', tx, ty)!;
  expect(link, 'link created in the destination plugin').toBeTruthy();
  expect(link.childGridId, 'the grid is SHARED, not copied').toBe(src.childGridId);
  expect(link.reference, 'the link renders dashed').toBe(true);

  // Deleting the link only unlinks: drag it to pane B's trashcan; the source
  // well in plugin "e2e" must survive byte-for-byte.
  const bAgain = (await gw.panes()).find((p) => p.id === b.id)!;
  await gw.clickScreen(bAgain.x + 20, bAgain.y + 20);
  await gw.deleteTileCell(tx, ty);
  expect(tileAt(await gw.getGrid(bNow.gridID), 'well', tx, ty), 'link gone').toBeUndefined();
  const srcAfter = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  expect(srcAfter, 'source well survived the unlink').toBeTruthy();
  expect(srcAfter.childGridId).toBe(src.childGridId);
});

test('right-drag a plugin tile off the landing page mounts it as a link', async ({ gw }) => {
  // Pane A enters "e2e"; the split pane B lands back on the node grid.
  await gw.enterPlugin('e2e');
  const a = await gw.focused();
  await gw.splitFocusedPaneVertical();
  const b = (await gw.panes()).find((p) => p.id !== a.id)!;

  // Locate the "second" plugin tile on pane B's node grid (cell coords from
  // the oracle — the node grid is a real grid). Focus pane B with a corner
  // click; its centered plugin tiles would swallow a center click as descent.
  await gw.clickScreen(b.x + 20, b.y + 20);
  const bNow = (await gw.panes()).find((p) => p.id === b.id)!;
  const ng = await gw.getGrid(bNow.gridID);
  const pluginTile = (ng.tiles ?? []).find((t: Tile) => t.altText === 'second')!;
  expect(pluginTile, 'plugin tile on the node grid').toBeTruthy();

  // Right-drag it into pane A's grid. Proto-JSON serializes int64 as strings
  // and omits zeros, hence the coercion. The target offsets VERTICALLY from
  // pane A's center: a horizontal offset at descent zoom can cross the
  // half-width pane's edge and land the drop in pane B instead (learned the
  // hard way — the drop then targeted the read-only node grid).
  const aCur = (await gw.panes()).find((p) => p.id === a.id)!;
  const tx = Math.round(aCur.cx);
  const ty = Math.round(aCur.cy) - 1;
  await gw.cloneDragAcrossPanes(b.id, Number(pluginTile.x ?? 0), Number(pluginTile.y ?? 0), a.id, tx, ty);

  const mount = tileAt(await gw.getGrid(a.gridID), 'well', tx, ty)!;
  expect(mount, 'mount link created in the destination grid').toBeTruthy();
  expect(mount.childGridId, "the link points at the plugin's root").toBe(pluginTile.childGridId);
  expect(mount.reference, 'mounts are links — deletes will not propagate').toBe(true);
  expect(mount.altText, 'the mount carries the plugin label').toBe('second');

  // Descend through the mount: pane A now shows the second plugin's grid.
  const aBack = (await gw.panes()).find((p) => p.id === a.id)!;
  await gw.clickScreen(aBack.x + 20, aBack.y + 20);
  await gw.descendCell(tx, ty);
  const aAfter = (await gw.panes()).find((p) => p.id === a.id)!;
  expect(aAfter.gridID, 'descent through the mount reaches the plugin').toBe(pluginTile.childGridId);
});
