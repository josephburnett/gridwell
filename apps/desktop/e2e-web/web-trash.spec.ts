import { test, expect } from './fixtures';
import { tileAt, getGrid } from '../e2e/oracle';

// The trashcan, end to end: a delete parks the tile, keeping its id, under a
// dated month well in the trash grid, which is a declared root menu entry whose
// swatch rides the + menu's top row beside its plugin. A delete inside the trash
// tree destroys for real. Everything crosses the standard API; the host and
// client know only that this is another root grid with a glyph.

test('delete parks in the dated trash; delete there is forever', async ({ gw, serve }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(doc).toBeTruthy();

  // Delete: gone from here, but moved rather than destroyed.
  await gw.deleteTileCell(cx, cy);
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx, cy), 'gone from the source grid').toBeUndefined();

  // The trashcan swatch is a declared root entry on the + menu's top row, and
  // clicking it descends like any other swatch.
  await gw.openPalette();
  const pal = await gw.palette();
  const trash = pal.items.find((i) => i.isPlugin && i.entry === 'trash');
  expect(trash, 'the + menu offers the declared trash root').toBeTruthy();
  await gw.clickPluginSwatch('trash');
  const troot = await gw.focused();

  // The bar knows the door you came through: the title is the entry's declared
  // label, which is config-owned and not renamable, and the level's crumb wears
  // the entry's declared glyph rather than a generic grid face.
  await expect.poll(async () => (await gw.barName()).label).toBe('trash');
  expect((await gw.barName()).editable, 'a declared entry is not renamable').toBe(false);
  const bar = await gw.bar();
  const rootCrumb = bar.segments.filter((s) => s.kind === 'chain' && s.anchor).pop();
  expect(rootCrumb?.glyph, 'the crumb wears the trash glyph').toBe('trash');

  // One dated month well, with the deleted tile parked inside under the same id.
  const tg = await gw.getGrid(troot.gridID);
  const wells = (tg.tiles ?? []).filter((t) => t.kind === 'well');
  expect(wells.length, 'one month well').toBe(1);
  expect(wells[0].altText).toMatch(/^\d{4}-\d{2}$/);
  const mg = await getGrid(serve.origin, wells[0].childGridId!);
  const parked = (mg.tiles ?? []).find((t) => t.id === doc.id);
  expect(parked, 'the same tile id, parked under the month').toBeTruthy();

  // Descend into the month and delete again: forever this time.
  await gw.descendCell(Number(wells[0].x ?? 0), Number(wells[0].y ?? 0));
  await gw.deleteTileCell(Number(parked!.x ?? 0), Number(parked!.y ?? 0));
  const after = await getGrid(serve.origin, wells[0].childGridId!);
  expect(
    (after.tiles ?? []).some((t) => t.id === doc.id),
    'the in-trash delete destroys',
  ).toBe(false);
});
