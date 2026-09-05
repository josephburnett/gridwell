import { test, expect } from './fixtures';

// A bare click on a palette swatch is the MENU's gesture. The popover floats
// over a live pane, so a swatch whose click nobody claimed used to fall
// through to the canvas gesture behind it — the bare-click navigation, at the
// popover's own coordinates. Clicking the well swatch with a well sitting
// behind it descended into that well: a pane moved somewhere the user never
// pointed at, because of what happened to be under a menu.
//
// Only the real app can see this: the fall-through was in the shim, between a
// pure verdict and a canvas hit-test, and what it descended into was a tile
// the test had to place under the popover.
test('clicking a swatch that only creates by dragging navigates nowhere', async ({ gw }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();

  // Where is the well swatch, and which grid cell sits under it? The cell
  // size is the distance between two neighbouring cell centers, so the
  // mapping needs no constant from the renderer.
  await gw.openPalette();
  const swatch = (await gw.palette()).items.find((i) => !i.isPlugin && i.kind === 'well')!;
  expect(swatch, 'the well swatch is on the menu').toBeTruthy();
  const origin = await gw.cellCenter(home.id, 0, 0);
  const oneOver = await gw.cellCenter(home.id, 1, 1);
  const cell = { w: oneOver.x - origin.x, h: oneOver.y - origin.y };
  const under = {
    x: Math.round((swatch.x + swatch.w / 2 - origin.x) / cell.w),
    y: Math.round((swatch.y + swatch.h / 2 - origin.y) / cell.h),
  };

  // Put a well there: created out in the open, then dragged under the menu.
  const spare = { x: Math.round(home.cx), y: Math.round(home.cy) };
  await gw.dragCreate('well', spare.x, spare.y);
  await gw.dragTileCell(spare.x, spare.y, under.x, under.y);
  const grid = await gw.getGrid(home.gridID);
  const well = (grid.tiles ?? []).find((t) => t.kind === 'well')!;
  expect([Number(well.x ?? 0), Number(well.y ?? 0)], 'the well sits under the swatch').toEqual([
    under.x,
    under.y,
  ]);

  // Click the well swatch. It creates only by being dragged, so the click
  // does nothing at all — and above all it does not descend into the tile
  // behind the popover.
  await gw.openPalette();
  await gw.clickPaletteSwatch('well');
  await gw.waitIdle();

  const after = await gw.focused();
  expect(after.placeDepth, 'the click pushed no frame').toBe(home.placeDepth);
  expect(after.gridID, 'the pane is on the same grid').toBe(home.gridID);
  expect((await gw.palette()).open, 'and the menu is still open').toBe(true);
});
