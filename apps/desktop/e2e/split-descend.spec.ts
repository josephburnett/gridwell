import { test, expect } from './fixtures';

// Ctrl + left-click on a well descends into it in a NEW pane: the pane
// splits below (the same programmatic split a link out of a live tile
// opens), the new lower pane takes focus and shows the child grid, and the
// original pane stays exactly where it was. A plain click still descends in
// place — ctrl is an additive ask, decided at press time
// (dragdrop.DropNavigateSplit).
test('ctrl+click descends in a new split pane; plain click stays in place', async ({ gw }) => {
  await gw.enterPlugin('home');
  const before = await gw.focused();
  const cx = Math.round(before.cx);
  const cy = Math.round(before.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);

  await gw.ctrlDescendCell(cx, cy);
  await expect.poll(async () => (await gw.panes()).length, {
    message: 'the ctrl-click split the pane',
  }).toBe(2);

  const panes = await gw.panes();
  const focused = panes.find((p) => p.focused)!;
  const original = panes.find((p) => p.id === before.id)!;
  expect(focused.id, 'the new pane took focus').not.toBe(before.id);
  expect(focused.placeDepth, 'the new pane descended one doorway').toBe(before.placeDepth + 1);
  expect(focused.gridID, 'into the well child grid').not.toBe(before.gridID);
  expect(original.placeDepth, 'the original pane did not move').toBe(before.placeDepth);
  expect(original.gridID, 'the original pane still shows its grid').toBe(before.gridID);
  expect(focused.y, 'the new pane is the lower half').toBeGreaterThan(original.y);

  // A plain click on the same well still descends in place: no third pane.
  await gw.focusPane(original);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID, {
    message: 'plain click descended in place',
  }).toBe(focused.gridID);
  expect((await gw.panes()).length, 'plain click made no pane').toBe(2);
});
