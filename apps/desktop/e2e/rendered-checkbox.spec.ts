import { test, expect } from './fixtures';
import { tileAt, getTileContent } from './oracle';

// Interactive task-list checkboxes (owner decision 2026-08-09, carving one
// control out of #218's read-only rendered view): clicking a checkbox in
// the rendered overlay flips its "[ ]"/"[x]" marker in the SOURCE, through
// the same content-store entry + debounced flush a keystroke uses. This
// spec crosses the whole seam — a DOM click in #gw-rendered-view ends as
// changed bytes on the server (ReadContent), both directions, while a
// code-fenced fake task proves the DOM→source index mapping skips
// non-tasks (the parity invariant unit-tested in client/markdown).

test('clicking rendered checkboxes toggles the source markers and persists', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.typeText('# Todo\n\n- [ ] alpha\n- [x] beta\n\n```\n- [ ] fenced, not a task\n```');
  await gw.toggleTextMode(); // rendered
  await gw.waitIdle();

  const view = window.locator('#gw-rendered-view');
  await expect(view).toBeVisible();
  const boxes = view.locator('input[type=checkbox]');
  // The fenced "- [ ]" is code, not a task — exactly two checkboxes.
  await expect(boxes).toHaveCount(2);
  await expect(boxes.nth(0)).not.toBeChecked();
  await expect(boxes.nth(1)).toBeChecked();

  const tileId = tileAt(await gw.getGrid(grid), 'text', cx, cy)!.id;
  const content = () => getTileContent(gw.origin, tileId);

  // Check alpha: the source marker flips and the flush lands it on the
  // server; the overlay re-renders from the toggled source.
  await boxes.nth(0).click();
  await expect(boxes.nth(0)).toBeChecked();
  await expect.poll(content, { timeout: 10_000 }).toContain('- [x] alpha');

  // Uncheck beta — the other direction, and the fenced text is untouched.
  await boxes.nth(1).click();
  await expect(boxes.nth(1)).not.toBeChecked();
  await expect.poll(content, { timeout: 10_000 }).toContain('- [ ] beta');
  expect(await content()).toContain('- [ ] fenced, not a task');

  // The raw editor shows the same truth — one content fact, no fork.
  await gw.toggleTextMode();
  const val = await window.evaluate(
    () => (document.getElementById('gw-text-editor') as HTMLTextAreaElement).value,
  );
  expect(val).toContain('- [x] alpha');
  expect(val).toContain('- [ ] beta');

  await gw.middleClickCell(cx, cy); // teardown ascent
});
