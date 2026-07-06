import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The + palette's name field: a name typed above the swatches becomes the
// created well's alt_text — the grid's name, a durable server fact. This
// crosses the whole seam: DOM input → wasm commit → CreateTile wire →
// localdb dispatch → store column → GetGrid oracle readback.
test('the palette name field labels the created grid', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Closed palette → no name input on screen.
  const name = window.locator('#gw-palette-name');
  await expect(name, 'input hidden while the palette is closed').toBeHidden();

  // Open, type a name, drag the well swatch onto the grid.
  await gw.openPalette();
  await expect(name, 'input floats over the open palette').toBeVisible();
  await name.fill('recipes');
  await gw.dragCreate('well', cx, cy);

  // Ground truth: the server stored the name as the well's alt_text.
  const w = tileAt(await gw.getGrid(grid), 'well', cx, cy);
  expect(w, 'well created at the drop cell').toBeTruthy();
  expect(w!.altText, "the grid's name is a server fact").toBe('recipes');

  // The commit closed the palette, which hides the input.
  await expect(name, 'input hidden after the drop commits').toBeHidden();

  // Reopening starts with a blank draft, and an unnamed create stays unnamed.
  await gw.openPalette();
  await expect(name, 'each open starts blank').toHaveValue('');
  await gw.dragCreate('well', cx + 2, cy);
  const plain = tileAt(await gw.getGrid(grid), 'well', cx + 2, cy);
  expect(plain, 'second well created').toBeTruthy();
  expect(plain!.altText ?? '', 'no name typed → unnamed').toBeFalsy();
});
