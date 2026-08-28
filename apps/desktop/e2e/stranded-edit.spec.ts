import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// An edit typed just before the pane leaves the document must still reach
// the server: the pending-edit fact is TILE-scoped (the content-store
// entry's dirty bit), never pane-scoped, so descending the pane elsewhere
// while the save debounce is still pending cannot strand it (charter §7 —
// the 2026-07-18 incident class). The old vehicle for this invariant was a
// rendered-mode embed jump; embeds and rendered editing died with #218, so
// the raw editor and an ordinary ascend-descend-elsewhere carry it now.

test('an edit typed right before leaving the doc still persists', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) - 1;

  // Two docs: A (the one we strand) and B (where the pane goes next).
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

  // Type the edit and leave INSIDE the save-debounce window: no waitIdle
  // between — the whole point is to leave with the save still pending.
  await gw.descendCell(cx, cy);
  await window.keyboard.type(' EDIT');
  const p = await gw.focused();
  await window.mouse.click(p.x + p.w / 2, p.y + p.h / 2, { button: 'middle' }); // leave: the edit is stranded NOW
  // The race that matters is typing→leaving; the ascent may settle (its
  // animation must, or the next descend computes cells mid-transition).
  await gw.waitIdle();
  await gw.descendCell(cx + 1, cy);
  await gw.waitIdle();
  expect((await gw.focused()).textFocus, 'the pane moved to the other doc').not.toBe(docA.id);

  // The edit belongs to doc A (tile-scoped) and must land server-side even
  // though no pane shows A anymore.
  await expect
    .poll(async () => gw.getTileContent(docA.id), { timeout: 10_000 })
    .toContain('EDIT');
});
