import { test, expect } from './fixtures';
import { tileAt, Tile } from './oracle';

// The federated space, end to end through the real stack:
//   - boot lands on the FIRST plugin in server.yaml (owner decision
//     2026-07-19); the node grid remains a real, read-only, server-owned
//     grid of dashed links — the federation surface an ssh mount lands on;
//   - right-dragging a well across a plugin boundary creates a LINK in the
//     destination (the grid is shared, never copied), and deleting the link
//     only unlinks;
//   - dragging a plugin swatch out of the + menu into a grid mounts that
//     plugin as a link well — the gesture the menu's plugin row exists for.

test.use({ extraPlugins: [{ kind: 'localdb', name: 'second' }] });

test('boot lands on the first plugin; the node grid stays a real grid of links', async ({ gw, window }) => {
  const pls = await gw.plugins();
  expect(pls.map((p) => p.label), 'both plugins configured, server.yaml order').toEqual(['e2e', 'second']);

  // Boot home: the FIRST plugin's root grid, not the node grid.
  const f = await gw.focused();
  expect(f.gridID, 'pane anchored at the first plugin root').toBe(pls[0].rootGridID);

  // The node grid still exists as the federation surface: a REAL grid
  // ("<node>/0") the oracle can read like any grid, one dashed link per
  // plugin.
  const nodeGrid = await window.evaluate(() => (window as any).__gridwellTest.nodeGrid());
  expect(nodeGrid, 'node identity learned').toMatch(/\/0$/);
  const g = await gw.getGrid(nodeGrid);
  const labels = (g.tiles ?? []).map((t: Tile) => t.altText).sort();
  expect(labels, 'tiles labeled by config name').toEqual(['e2e', 'second']);
  for (const t of g.tiles ?? []) {
    expect(t.reference, `plugin tile ${t.altText} is a link (dashed)`).toBe(true);
    expect(t.kind).toBe('well');
  }
});

test('right-drag links a well across plugins; deleting the link never touches the source', async ({ gw }) => {
  // Split at boot: both panes clone the boot grid (the first plugin's root);
  // focus lands on the NEW pane. Focus panes with CORNER clicks throughout:
  // pane centers hold tiles, and a center click on a focused pane would
  // descend.
  await gw.plugins();
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const a0 = (await gw.panes()).find((p) => p.id !== bId)!;

  // Pane A: enter "e2e", create a well OFF-center (cy-1) so later clicks
  // never land on it.
  await gw.clickScreen(a0.x + 20, a0.y + 20);
  await gw.enterPlugin('e2e');
  const a = (await gw.panes()).find((p) => p.id === a0.id)!;
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const src = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  expect(src, 'source well created').toBeTruthy();

  // Pane B: enter "second".
  const b = (await gw.panes()).find((p) => p.id === bId)!;
  await gw.clickScreen(b.x + 20, b.y + 20);
  await gw.enterPlugin('second');
  const bNow = (await gw.panes()).find((p) => p.id === bId)!;
  const tx = Math.round(bNow.cx);
  const ty = Math.round(bNow.cy) - 1;

  // The link gesture: right-drag the well from pane A into pane B.
  await gw.clickScreen(a.x + 20, a.y + 20);
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

test('drag a plugin swatch out of the + menu mounts it as a link', async ({ gw }) => {
  // Boot lands on "e2e" (the first plugin). Drag the "second" plugin's menu
  // swatch onto a cell OFF-center so later clicks never land on it.
  const pls = await gw.plugins();
  const second = pls.find((p) => p.label === 'second')!;
  const f = await gw.focused();
  const tx = Math.round(f.cx);
  const ty = Math.round(f.cy) - 1;
  await gw.openPalette();
  await gw.dragPluginLink('second', tx, ty);

  const mount = tileAt(await gw.getGrid(f.gridID), 'well', tx, ty)!;
  expect(mount, 'mount link created in the destination grid').toBeTruthy();
  expect(mount.childGridId, "the link points at the plugin's root").toBe(second.rootGridID);
  expect(mount.reference, 'mounts are links — deletes will not propagate').toBe(true);
  expect(mount.altText, 'the mount carries the plugin label').toBe('second');

  // Descend through the mount: the pane now shows the second plugin's grid.
  await gw.descendCell(tx, ty);
  const after = await gw.focused();
  expect(after.gridID, 'descent through the mount reaches the plugin').toBe(second.rootGridID);
});
