import { test, expect } from './fixtures';
import { tileAt, updateText } from './oracle';

// ctrl + right-drag is the link gesture: the modifier flips the right button's
// meaning from copy to link, inside one id namespace as well as across one.
// The gesture is opaque on the canvas, so the server oracle is the ground truth
// for what landed and for what did not change about the source. These cross the
// seam from gesture to verdict to wire verb: the canvas classifies the press
// (client/dragdrop.Intent), DecideDrop verdicts DropLink, and the commit fires
// CreateLeafLink or CreateTile-with-a-child-grid instead of CopyTile. A unit
// test on either side alone would not catch a gesture wired to the wrong verb.

test('ctrl+right-drag links inside one namespace; plain right-drag still copies', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Distinctive bytes make both "the link resolves to the source" and "the
  // source is byte-identical afterwards" observable.
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const src = tileAt(await gw.getGrid(grid), 'text', cx, cy);
  expect(src, 'created the source text tile').toBeTruthy();
  await updateText(gw.origin, src!.id, Number(src!.version ?? 0), 'the original bytes');
  const before = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;

  // ── LINK ────────────────────────────────────────────────────────────────
  await gw.linkTileCell(cx, cy, cx, cy + 2);
  let snap = await gw.getGrid(grid);
  const link = tileAt(snap, 'text', cx, cy + 2);
  expect(link, 'ctrl+right-drag left a tile at the drop cell').toBeTruthy();
  expect(link!.id, 'the link is its own row, not the source').not.toBe(before.id);
  expect(link!.linkTargetId, 'the link names the source tile').toBe(before.id);
  // link is the one authoritative "is a link" signal, the same bit the
  // renderer dashes the border from and the store keys unlink on.
  expect(link!.link, 'the link is marked dashed').toBe(true);

  // The rule: everything the user did not touch is byte-for-byte the same.
  const after = tileAt(snap, 'text', cx, cy)!;
  expect(after.id, 'source id unchanged').toBe(before.id);
  expect(Number(after.version ?? 0), 'source version unchanged (no content claim)')
    .toBe(Number(before.version ?? 0));
  expect(Number(after.w), 'source footprint unchanged').toBe(Number(before.w));
  expect(Number(after.h), 'source footprint unchanged').toBe(Number(before.h));
  expect(await gw.getTileContent(before.id), 'source bytes unchanged')
    .toBe('the original bytes');

  // A link owns no bytes: reading it resolves through link_target_id to the
  // source.
  expect(await gw.getTileContent(link!.id), 'the link reads the source bytes')
    .toBe('the original bytes');

  // ── PLAIN RIGHT-DRAG STILL COPIES ───────────────────────────────────────
  // Without ctrl the same gesture copies: a new row owning its own bytes and
  // naming no target.
  await gw.copyTileCell(cx, cy, cx + 2, cy);
  snap = await gw.getGrid(grid);
  const copy = tileAt(snap, 'text', cx + 2, cy);
  expect(copy, 'right-drag landed a tile').toBeTruthy();
  expect(copy!.id, 'the copy is its own row').not.toBe(before.id);
  expect(copy!.linkTargetId ?? '', 'a copy names no link target').toBe('');
  expect(copy!.link ?? false, 'a copy is not a link').toBeFalsy();
  expect(await gw.getTileContent(copy!.id), 'the copy copied the bytes')
    .toBe('the original bytes');

  // ── DELETING A LINK UNLINKS ─────────────────────────────────────────────
  await gw.deleteTileCell(cx, cy + 2);
  snap = await gw.getGrid(grid);
  expect(tileAt(snap, 'text', cx, cy + 2), 'the link row is gone').toBeFalsy();
  expect(tileAt(snap, 'text', cx, cy), 'the source survives the unlink').toBeTruthy();
  expect(await gw.getTileContent(before.id), 'the source keeps its bytes')
    .toBe('the original bytes');
});

test('ctrl+right-drag on a well links the same child grid, not a copy of it', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(grid), 'well', cx, cy);
  expect(well, 'created the source well').toBeTruthy();
  const childGrid = String(well!.childGridId);
  expect(childGrid, 'an interior well owns a child grid').toBeTruthy();

  await gw.linkTileCell(cx, cy, cx, cy + 2);
  const snap = await gw.getGrid(grid);
  const linkWell = tileAt(snap, 'well', cx, cy + 2);
  expect(linkWell, 'ctrl+right-drag left a well at the drop cell').toBeTruthy();
  expect(linkWell!.id).not.toBe(well!.id);
  // The doorway leads to the SAME grid, a second way in. A right-drag copy
  // would have deep-copied the subtree into a fresh grid.
  expect(String(linkWell!.childGridId), 'the link opens the source well\'s own child grid')
    .toBe(childGrid);
  expect(linkWell!.link, 'the link well is dashed').toBe(true);

  const srcAfter = tileAt(snap, 'well', cx, cy)!;
  expect(String(srcAfter.childGridId), 'the source well still owns its child grid')
    .toBe(childGrid);
  expect(srcAfter.link ?? false, 'the source is still owned content, not a link')
    .toBeFalsy();
});
