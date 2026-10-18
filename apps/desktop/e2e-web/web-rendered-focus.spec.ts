import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// A text pane left in rendered mode must stay rendered when focus moves to a
// sibling. The rendered view is a focused-pane DOM overlay, so an unfocused
// pane falling back to raw source on canvas would be a visible flip the user
// never asked for; it paints the rendered raster instead. The oracle is the
// renderedPreviews hook's per-tile panePaints counter.
test('a rendered pane stays rendered when focus moves to a sibling', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.typeText('# A Big Heading\n\nrendered body text');

  await gw.toggleTextMode();
  await expect
    .poll(async () => (await gw.focused()).textMode)
    .toBe('rendered');

  // The original pane keeps its rendered descent and loses the DOM overlay.
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();

  // A pane that flipped to raw leaves panePaints at 0 forever.
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

  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  const orig = panes.find((p: any) => p.textFocus === created.id);
  expect(orig, 'the original pane still holds the rendered descent').toBeTruthy();
});
