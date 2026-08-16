import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// #261 — the charter's first face at the pane seam: a text pane left in
// RENDERED mode must STAY rendered when focus moves to a sibling pane.
// The rendered view is a focused-pane DOM overlay; before the fix the
// unfocused pane fell back to painting RAW source on canvas — a visible
// flip the user never asked for. Now the uncovered pane paints the
// rendered RASTER (#233's cache); the overlay↔raster swap is invisible.
// The oracle is the renderedPreviews testhook's panePaints counter — the
// pane path attributes its raster paints per tile, so "stayed rendered"
// is a counted fact, not a pixel guess.
test('a rendered pane stays rendered when focus moves to a sibling', async ({ gw, window }) => {
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.typeText('# A Big Heading\n\nrendered body text');

  // Flip to RENDERED mode (the bar-slot toggle) and wait for the overlay.
  await gw.toggleTextMode();
  await expect
    .poll(async () => (await gw.focused()).textMode)
    .toBe('rendered');

  // Split: the sibling takes focus; the original pane keeps its rendered
  // descent but loses the DOM overlay.
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();

  // The unfocused pane must paint the rendered RASTER — panePaints
  // climbs and the raster is decoded. If the pane had flipped to raw,
  // the counter would stay 0 forever.
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

  // And the pane's MODE fact never changed (the flip was only ever a
  // presentation bug, but pin the fact too).
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  const orig = panes.find((p: any) => p.textFocus === created.id);
  expect(orig, 'the original pane still holds the rendered descent').toBeTruthy();
});
