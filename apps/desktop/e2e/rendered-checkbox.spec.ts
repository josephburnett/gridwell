import { test, expect } from './fixtures';
import { tileAt, getTileContent } from './oracle';

// Task-list checkboxes are the one interactive control in the otherwise
// read-only rendered view. Clicking one flips its "[ ]" or "[x]" marker in the
// source, through the same content-store entry and debounced flush a keystroke
// uses. A DOM click in #gw-rendered-view has to end as changed bytes on the
// server. The DOM-to-source index mapping that skips non-tasks is unit-tested
// in client/markdown.

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
  // Pasted, not typed: typed, Enter would continue each list item.
  await window.keyboard.insertText('# Todo\n\n- [ ] alpha\n- [x] beta\n\n```\n- [ ] fenced, not a task\n```');
  await gw.waitIdle();
  await gw.toggleTextMode(); // rendered
  await gw.waitIdle();

  const view = window.locator('#gw-rendered-view');
  await expect(view).toBeVisible();
  const boxes = view.locator('input[type=checkbox]');
  // The fenced "- [ ]" is code, not a task, so there are exactly two checkboxes.
  await expect(boxes).toHaveCount(2);
  await expect(boxes.nth(0)).not.toBeChecked();
  await expect(boxes.nth(1)).toBeChecked();

  const tileId = tileAt(await gw.getGrid(grid), 'text', cx, cy)!.id;
  const content = () => getTileContent(gw.origin, tileId);

  // The marker flips, the flush lands it on the server, and the overlay
  // re-renders from the toggled source.
  await boxes.nth(0).click();
  await expect(boxes.nth(0)).toBeChecked();
  await expect.poll(content, { timeout: 10_000 }).toContain('- [x] alpha');

  // The other direction, and the fenced text stays untouched.
  await boxes.nth(1).click();
  await expect(boxes.nth(1)).not.toBeChecked();
  await expect.poll(content, { timeout: 10_000 }).toContain('- [ ] beta');
  expect(await content()).toContain('- [ ] fenced, not a task');

  // The raw editor reads the same content fact.
  await gw.toggleTextMode();
  const val = await window.evaluate(
    () => (document.getElementById('gw-text-editor') as HTMLTextAreaElement).value,
  );
  expect(val).toContain('- [x] alpha');
  expect(val).toContain('- [ ] beta');

  await gw.middleClickCell(cx, cy); // teardown ascent
});
