import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Enter on a markdown list line continues the list. The decision is
// textedit.ContinueList's table; this proves the textarea keydown applies it
// and the result reaches the server through the ordinary save chain.
test('Enter continues bullet and checkbox lists, ends them on an empty item', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();
  await gw.descendCell(cx, cy);

  // Each Enter after an item's text starts the next item; the Enter on the
  // empty item that follows removes its marker and ends the list.
  await gw.typeText('- one\ntwo\n\nafter\n');
  await gw.typeText('  - [x] done\nopen\n\n');
  await gw.typeText('1. first\nsecond\n\n');
  await gw.typeText('- plain');
  await window.keyboard.press('Shift+Enter');
  await gw.typeText('line');

  const want = [
    '- one',
    '- two',
    'after',
    '  - [x] done',
    '  - [ ] open',
    '1. first',
    '2. second',
    '- plain',
    'line',
  ].join('\n');
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toBe(want);
});
