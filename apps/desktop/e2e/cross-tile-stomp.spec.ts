import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import type { GridwellDriver } from './driver';

// The cross-TILE stomp (2026-07-18 incident): two panes text-descended into
// two different text tiles share ONE textarea singleton, bound to whichever
// tile was descended into last. Every bulk flush path (pane collapse,
// workspace enter/leave — flushDroppedSubtree → saveTextBeforeAscent) reads
// that one singleton's value and posts it as the flushed pane's tile content,
// with no check that the singleton is actually bound to THAT tile
// (lastTextareaTileID). The debounced-keystroke path has exactly this guard
// (textedit.ShouldDebouncedSaveFire); the flush path did not — so collapsing
// the pane holding tile A while the textarea is bound to tile B saved B's
// bytes as A's content. The save claimed A's own valid basis, so the server's
// version check waved it through: silent, total content replacement.
//
// Why this was not caught: every existing text-save spec drives ONE
// text-descended pane, so the singleton is always bound to the tile being
// flushed. No spec ever held two text descents alive at once and then ran a
// bulk flush over the un-bound one.

// seedTile creates a markdown tile at (cx, cy), types seed into it, ascends,
// and waits for the content to persist. Pane must be at grid level.
async function seedTile(
  gw: GridwellDriver,
  gridID: string,
  cx: number,
  cy: number,
  seed: string,
): Promise<string> {
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(gridID), 'text', cx, cy)!;
  expect(created, `markdown tile created at (${cx},${cy})`).toBeTruthy();
  await gw.descendCell(cx, cy);
  await gw.typeText(seed);
  await gw.ascendViaCrumb(); // bar-crumb ascent out of the text descent
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toBe(seed);
  return created.id;
}

test('collapsing a text-descended pane never saves another tile\'s buffer into it', async ({ gw }) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Two text tiles, either side of the viewport center (the center cell stays
  // empty so focus clicks stay pure).
  const alphaID = await seedTile(gw, f.gridID, cx - 1, cy, 'alpha alpha');
  const bravoID = await seedTile(gw, f.gridID, cx + 1, cy, 'bravo bravo');

  // Two panes over the same grid; descend LEFT into alpha, RIGHT into bravo.
  // The textarea singleton ends bound to bravo (the last text descent).
  await gw.splitFocusedPaneVertical();
  const panes = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes.length, 'split produced two panes').toBe(2);

  await gw.focusPane(panes[0]);
  await gw.descendCell(cx - 1, cy);
  await expect
    .poll(async () => gw.textareaValue(), { timeout: 10_000 })
    .toBe('alpha alpha');

  const panesAfter = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  await gw.focusPane(panesAfter[1]);
  await gw.descendCell(cx + 1, cy);
  await expect
    .poll(async () => gw.textareaValue(), { timeout: 10_000 })
    .toBe('bravo bravo');

  // Collapse the LEFT pane — the one descended into alpha, which the textarea
  // singleton is NOT bound to. The collapse's flush must persist alpha's own
  // content (a no-op), never the singleton's bravo bytes.
  //
  // Grab the divider from the RIGHT side so focus (and the textarea binding)
  // stays on bravo through the gesture — a right-press inside the left pane
  // would transfer focus there first, rebinding the singleton to alpha and
  // masking the seam. Dragging from the right pane's divider band to the left
  // edge collapses the left side (gesture.CollapseA).
  const [leftPane, rightPane] = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const midY = rightPane.y + rightPane.h / 2;
  await gw.leftDragScreen(rightPane.x + 3, midY, leftPane.x + 6, midY); // #203: left owns crush-to-close
  expect((await gw.panes()).length, 'collapsed back to one pane').toBe(1);

  // Server truth: both tiles keep their own words.
  await expect
    .poll(async () => gw.getTileContent(alphaID), { timeout: 10_000 })
    .toBe('alpha alpha');
  expect(await gw.getTileContent(bravoID)).toBe('bravo bravo');
});
