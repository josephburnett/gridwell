import { test, expect } from './fixtures';

// dropTargetAt is the one owner of where a menu swatch may land, and it
// refuses a pane descended into a tile, because there is no grid on screen to
// drop into. The commit reads that same verdict through dropInputAt rather than
// resolving the destination again, so a release over a descended pane cannot
// create a tile in the grid hidden behind it. The two tests are a pair: the
// first pins that this gesture at this point does create a tile, so the second
// cannot pass by doing nothing.

// dropPoint is the top edge of the right pane, the farthest the cursor can get
// from the view center, where a content descent parks the tile it is inside. At
// a text descent's zoom a point near the center maps back to that tile's own
// cell, where an overlap check would refuse the drop for the wrong reason.
const dropPoint = (p: { x: number; w: number; y: number }) => ({
  x: p.x + p.w / 2,
  y: p.y + 6,
});

test('a swatch dropped into another pane that shows a grid lands there', async ({ gw }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();

  await gw.splitFocusedPaneVertical();
  const panes = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes.length, 'two panes side by side').toBe(2);
  const [left, right] = panes;

  await gw.focusPane(left);
  await gw.openMenu();
  const pt = dropPoint(right);
  await gw.dragCreateToScreen('well', pt.x, pt.y);

  const after = await gw.getGrid(home.gridID);
  expect(
    (after.tiles ?? []).filter((t) => t.kind === 'well').length,
    'the drop landed in the pane under the cursor',
  ).toBe(1);
});

test('a swatch dropped over a pane descended into a tile creates nothing', async ({ gw }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const before = await gw.getGrid(home.gridID);
  expect((before.tiles ?? []).length, 'the text tile landed').toBe(1);

  // The right pane descends into that tile, with the home grid still behind it.
  await gw.splitFocusedPaneVertical();
  const panes = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes.length, 'two panes side by side').toBe(2);
  const [left, right] = panes;

  await gw.focusPane(right);
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();
  expect(inside.id, 'the right pane is the one that descended').toBe(right.id);
  // textFocus is the persisted blob's spelling of "the content tile this pane
  // is inside" (ARCHITECTURE.md): the pane is in a content frame.
  expect(inside.textFocus, 'it is inside the text tile').not.toBe('');

  // A swatch dragged out of the left pane's menu and released over the descended
  // right pane resolves no drop target, so nothing is created anywhere.
  await gw.focusPane(left);
  await gw.openMenu();
  const pt = dropPoint(right);
  await gw.dragCreateToScreen('well', pt.x, pt.y);

  const after = await gw.getGrid(home.gridID);
  expect(
    (after.tiles ?? []).length,
    'the grid behind the descent gained no tile from the refused drop',
  ).toBe((before.tiles ?? []).length);
  expect(
    (after.tiles ?? []).filter((t) => t.kind === 'well').length,
    'and no well anywhere on it',
  ).toBe(0);
});
