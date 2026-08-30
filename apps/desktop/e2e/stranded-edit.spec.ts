import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// An edit typed just before the pane leaves the document must still reach the
// server. The pending-edit fact is tile-scoped, living on the content-store
// entry's dirty bit, and never pane-scoped, so moving the pane elsewhere while
// the save debounce is still pending cannot strand it.

test('an edit typed right before leaving the doc still persists', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) - 1;

  // Two docs: A, the one to strand, and B, where the pane goes next.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const docA = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.typeText('the doc');
  await gw.ascendViaCrumb();
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 1, cy);
  await gw.descendCell(cx + 1, cy);
  await gw.typeText('the other doc');
  await gw.ascendViaCrumb();

  // Type the edit and leave inside the save-debounce window, with no waitIdle
  // between: the point is to leave with the save still pending.
  await gw.descendCell(cx, cy);
  await window.keyboard.type(' EDIT');
  const p = await gw.focused();
  await window.mouse.click(p.x + p.w / 2, p.y + p.h / 2, { button: 'middle' }); // leave with the edit still pending
  // The race that matters is typing then leaving. The ascent may settle, and its
  // animation must, or the next descend computes cells mid-transition.
  await gw.waitIdle();
  await gw.descendCell(cx + 1, cy);
  await gw.waitIdle();
  expect((await gw.focused()).textFocus, 'the pane moved to the other doc').not.toBe(docA.id);

  // The edit belongs to doc A, being tile-scoped, and must land server-side even
  // though no pane shows A any more.
  await expect
    .poll(async () => gw.getTileContent(docA.id), { timeout: 10_000 })
    .toContain('EDIT');
});
