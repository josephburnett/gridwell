import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// A text pane left in rendered mode must stay rendered when focus moves to a
// sibling pane. The rendered view is a focused-pane DOM overlay, and an
// unfocused pane that falls back to painting raw source on canvas is a visible
// flip the user never asked for. The uncovered pane paints the rendered raster
// instead, so the swap between overlay and raster is invisible. The oracle is the
// renderedPreviews hook's panePaints counter: the pane path attributes its raster
// paints per tile, so staying rendered is a counted fact rather than a pixel
// guess.
test('a rendered pane stays rendered when focus moves to a sibling', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.typeText('# A Big Heading\n\nrendered body text');

  // Flip to rendered mode with the bar-slot toggle and wait for the overlay.
  await gw.toggleTextMode();
  await expect
    .poll(async () => (await gw.focused()).textMode)
    .toBe('rendered');

  // Split: the sibling takes focus, and the original pane keeps its rendered
  // descent but loses the DOM overlay.
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();

  // The unfocused pane must paint the rendered raster: panePaints climbs and the
  // raster decodes. A pane that flipped to raw leaves the counter at 0 forever.
  await expect
    .poll(
      async () => {
        const prev = await window.evaluate(() => (window as any).__gridwellTest.renderedPreviews());
        const e = prev[created.id];
        return !!(e && e.ready && e.panePaints > 0);
      },
      { timeout: 15_000 },
    )
    .toBe(true);

  // And the pane's mode fact never changed.
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  const orig = panes.find((p: any) => p.textFocus === created.id);
  expect(orig, 'the original pane still holds the rendered descent').toBeTruthy();
});
