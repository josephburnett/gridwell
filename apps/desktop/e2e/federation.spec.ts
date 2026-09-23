import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The federated space through the real stack. Boot lands on the node's own
// home, the first row of the menu list. Across the boundary a left-drag makes a
// link in the destination and the source stays put, since there is no
// cross-plugin move; a right-drag clones; deleting a link only unlinks; editing
// through a leaf link lands on the one shared copy of the bytes. Dragging a
// menu swatch into a grid mounts that namespace as a link well.

test.use({ extraNodes: ['second'] });

test('boot lands on the first plugin', async ({ gw }) => {
  const pls = await gw.plugins();
  expect(pls.map((p) => p.label), 'both plugins configured, server.yaml order').toEqual(['home', 'second']);

  // Boot lands on the home row's root grid.
  const f = await gw.focused();
  expect(f.gridID, 'pane anchored at the first plugin root').toBe(pls[0].rootGridID);
});

// twoPanesTwoPlugins splits the boot pane and enters "home" in pane A and
// "second" in pane B. The returned cells are off-center so later clicks never
// land on them, and panes are focused with corner clicks because a center click
// on a focused pane would descend.
async function twoPanesTwoPlugins(gw: any) {
  await gw.plugins();
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const a0 = (await gw.panes()).find((p: any) => p.id !== bId)!;

  await gw.clickScreen(a0.x + 20, a0.y + 20);
  await gw.enterPlugin('home');
  const a = (await gw.panes()).find((p: any) => p.id === a0.id)!;
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;

  const b = (await gw.panes()).find((p: any) => p.id === bId)!;
  await gw.clickScreen(b.x + 20, b.y + 20);
  await gw.enterPlugin('second');
  const bNow = (await gw.panes()).find((p: any) => p.id === bId)!;
  const tx = Math.round(bNow.cx);
  const ty = Math.round(bNow.cy) - 1;
  return { a, b: bNow, cx, cy, tx, ty };
}

test('left-drag links a well across nodes; the source stays; deleting the link never touches it', async ({ gw }) => {
  const { a, b, cx, cy, tx, ty } = await twoPanesTwoPlugins(gw);

  // The source well goes on the far node. A link can point from home into the
  // far node; the far node cannot route back into this node's home.
  await gw.clickScreen(b.x + 20, b.y + 20);
  await gw.openPalette();
  await gw.dragCreate('well', tx, ty);
  const src = tileAt(await gw.getGrid(b.gridID), 'well', tx, ty)!;
  expect(src, 'source well created').toBeTruthy();

  // Left-drag the well from pane B into pane A, which is home.
  await gw.leftDragAcrossPanes(b.id, tx, ty, a.id, cx, cy);

  const link = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  expect(link, 'link created in home').toBeTruthy();
  expect(link.childGridId, 'the grid is SHARED, not copied').toBe(src.childGridId);
  expect(link.reference, 'the link renders dashed').toBe(true);

  const srcStill = tileAt(await gw.getGrid(b.gridID), 'well', tx, ty)!;
  expect(srcStill, 'source well stayed put — a cross-node left-drag is a link, not a move').toBeTruthy();
  expect(srcStill.id).toBe(src.id);

  await gw.clickScreen(a.x + 20, a.y + 20);
  await gw.deleteTileCell(cx, cy);
  expect(tileAt(await gw.getGrid(a.gridID), 'well', cx, cy), 'link gone').toBeUndefined();
  const srcAfter = tileAt(await gw.getGrid(b.gridID), 'well', tx, ty)!;
  expect(srcAfter, 'source well survived the unlink').toBeTruthy();
  expect(srcAfter.childGridId).toBe(src.childGridId);
});

// Flaked once on CI (2026-09-21, run 35618756780) at the deep-copy read
// below: waitIdle() could not see a CloneTile in flight, so the spec read the
// server oracle mid-write. See docs/flake-ledger.md; closed in the client, not
// here.
test('right-drag deep-copies a solid well across nodes; a text left-drag links and edits through', async ({ gw }) => {
  const { a, b, cx, cy, tx, ty } = await twoPanesTwoPlugins(gw);

  await gw.clickScreen(b.x + 20, b.y + 20);
  await gw.openPalette();
  await gw.dragCreate('well', tx, ty);
  await gw.openPalette();
  await gw.dragCreate('markdown', tx - 1, ty);
  const srcWell = tileAt(await gw.getGrid(b.gridID), 'well', tx, ty)!;
  const srcText = tileAt(await gw.getGrid(b.gridID), 'text', tx - 1, ty)!;
  expect(srcWell && srcText, 'sources created').toBeTruthy();

  await gw.cloneDragAcrossPanes(b.id, tx, ty, a.id, cx, cy);
  const copied = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  expect(copied, 'the deep copy landed in home').toBeTruthy();
  expect(copied.reference ?? false, 'the copy is SOLID (owned), not a link').toBe(false);
  expect(copied.childGridId, 'an independent subtree, not the shared grid').not.toBe(srcWell.childGridId);
  expect(tileAt(await gw.getGrid(b.gridID), 'well', tx, ty), 'source well untouched').toBeTruthy();

  // One cell over, since the deep copy above now occupies (cx, cy).
  await gw.leftDragAcrossPanes(b.id, tx - 1, ty, a.id, cx - 1, cy);
  const link = tileAt(await gw.getGrid(a.gridID), 'text', cx - 1, cy)!;
  expect(link, 'leaf link created in home').toBeTruthy();
  expect(link.reference, 'the leaf link renders dashed').toBe(true);
  expect(link.linkTargetId, 'the link names the source tile as content owner').toBe(srcText.id);
  expect(tileAt(await gw.getGrid(b.gridID), 'text', tx - 1, ty), 'source text stayed put').toBeTruthy();

  // Editing through the link must land on the source tile's stored body.
  await gw.clickScreen(a.x + 20, a.y + 20);
  await gw.descendCell(cx - 1, cy);
  const marker = 'edited-through-the-link';
  await gw.typeText(marker);
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => gw.getTileContent(srcText.id), { timeout: 10_000 })
    .toContain(marker);
});

test('drag a plugin swatch out of the + menu mounts it as a link', async ({ gw }) => {
  // The target cell is off-center so later clicks never land on it.
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

  await gw.descendCell(tx, ty);
  const after = await gw.focused();
  expect(after.gridID, 'descent through the mount reaches the plugin').toBe(second.rootGridID);
});
