import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { settle, timeToLand } from './cadence';

// Drives the text-editing gesture end to end: create a markdown tile, descend
// into it, type, ascend, and assert the typed content reached the server through
// ReadContent. The edit logic is unit-tested in client/textedit and
// client/markdown; this proves the wiring from keystrokes to a durable body.
test('typing into a descended text tile persists to the server', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  // The bar takes the text family's shades from the same classifier as the pane
  // border, so the band and the frame cannot disagree.
  await gw.descendCell(cx, cy);
  const themed = await window.evaluate(() => (window as any).__gridwellTest.bar());
  expect(themed.band, 'text-family band').toBe('#1b2213');
  expect(themed.button, 'text-family button').toBe('#8aa05a');
  // The rendered and raw toggle is a DOM element in the same slot, and it must
  // wear the same family shades as the canvas buttons. Baking a color into its
  // style at creation would be a second copy of the theme fact.
  const toggle = window.locator('#gw-text-toggle');
  await expect(toggle).toBeVisible();
  await expect
    .poll(async () =>
      toggle.evaluate((el: HTMLElement) => getComputedStyle(el).backgroundColor))
    .toBe('rgb(138, 160, 90)'); // #8aa05a, the text-family button hue
  // The save is debounced, and nothing can reach the server before that wait
  // elapses, so one keystroke times it from below off the client's own value.
  const c = await gw.cadences();
  const marker = 'gridwell-e2e-typed';
  const saved = async () => {
    try {
      return (await gw.getTileContent(created.id)).includes(marker[0]);
    } catch {
      return false; // an untouched tile has no content to read yet
    }
  };
  await gw.waitIdle();
  await settle(window, c.textSaveMs);
  const landed = await timeToLand(
    window,
    () => window.keyboard.type(marker[0]),
    { saved },
    c.textSaveMs * 20,
  );
  expect(landed.saved, 'the save waited out its debounce').toBeGreaterThanOrEqual(c.textSaveMs);
  await gw.typeText(marker.slice(1));

  // The ascent flushes the edit.
  await gw.ascendViaCrumb();

  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain(marker);
});
