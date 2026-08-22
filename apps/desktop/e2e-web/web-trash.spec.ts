import { test, expect } from './fixtures';
import { tileAt, getGrid } from '../e2e/oracle';

// The local plugin's trashcan (#262), end to end: delete PARKS the tile —
// same id — under a dated month well in the trash grid (a declared ROOT
// menu entry, #258's other shape: the swatch rides the + menu top row
// beside the plugin); a delete INSIDE the trash tree destroys for real.
// Everything crosses the standard API — the host and client know only
// "another root grid with a glyph".

test('delete parks in the dated trash; delete there is forever', async ({ gw, serve }) => {
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(doc).toBeTruthy();

  // Delete: gone from here, but it MOVED — it didn't die.
  await gw.deleteTileCell(cx, cy);
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx, cy), 'gone from the source grid').toBeUndefined();

  // The trashcan swatch is a declared root entry on the + menu top row;
  // clicking it descends like any plugin swatch.
  await gw.openPalette();
  const pal = await gw.palette();
  const trash = pal.items.find((i) => i.isPlugin && i.entry === 'trash');
  expect(trash, 'the + menu offers the declared trash root').toBeTruthy();
  await gw.clickPluginSwatch('trash');
  const troot = await gw.focused();

  // The bar knows the DOOR you came through (#263/#264): the title is the
  // entry's declared label (config-owned, not renamable), and the level's
  // crumb wears the entry's declared glyph — not a generic grid face.
  await expect.poll(async () => (await gw.barName()).label).toBe('trash');
  expect((await gw.barName()).editable, 'a declared entry is not renamable').toBe(false);
  const bar = await gw.bar();
  const rootCrumb = bar.segments.filter((s) => s.kind === 'chain' && s.anchor).pop();
  expect(rootCrumb?.glyph, 'the crumb wears the trash glyph').toBe('trash');

  // One dated month well; the deleted tile parked inside, SAME id.
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
