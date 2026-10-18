import { test, expect } from './fixtures';

// A bare click on a menu swatch belongs to the menu. The popover floats over
// a live pane, so a swatch whose click nobody claims must not fall through to
// the canvas navigation at the popover's own coordinates. The seam is in the
// shim, between a pure verdict and a canvas hit-test, so only the real app can
// see it, and the tile behind the popover has to be placed there.
test('clicking a swatch that only creates by dragging navigates nowhere', async ({ gw }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();

  // The cell size is the distance between two neighbouring cell centers, so
  // finding the cell under the swatch needs no constant from the renderer.
  await gw.openMenu();
  const swatch = (await gw.menu()).items.find((i) => !i.isPlugin && i.kind === 'door')!;
  expect(swatch, 'the door swatch is on the menu').toBeTruthy();
  const origin = await gw.cellCenter(home.id, 0, 0);
  const oneOver = await gw.cellCenter(home.id, 1, 1);
  const cell = { w: oneOver.x - origin.x, h: oneOver.y - origin.y };
  const under = {
    x: Math.round((swatch.x + swatch.w / 2 - origin.x) / cell.w),
    y: Math.round((swatch.y + swatch.h / 2 - origin.y) / cell.h),
  };

  // The door is created out in the open, then dragged under the menu.
  const spare = { x: Math.round(home.cx), y: Math.round(home.cy) };
  await gw.dragCreate('door', spare.x, spare.y);
  await gw.dragTileCell(spare.x, spare.y, under.x, under.y);
  const grid = await gw.getGrid(home.gridID);
  const door = (grid.tiles ?? []).find((t) => t.kind === 'door')!;
  expect([Number(door.x ?? 0), Number(door.y ?? 0)], 'the door sits under the swatch').toEqual([
    under.x,
    under.y,
  ]);

  // The door swatch creates only by being dragged, so the click must do nothing
  // and must not descend into the tile behind the popover.
  await gw.openMenu();
  await gw.clickMenuSwatch('door');
  await gw.waitIdle();

  const after = await gw.focused();
  expect(after.placeDepth, 'the click pushed no frame').toBe(home.placeDepth);
  expect(after.gridID, 'the pane is on the same grid').toBe(home.gridID);
  expect((await gw.menu()).open, 'and the menu is still open').toBe(true);
});
