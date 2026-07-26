import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import type { GridwellDriver } from './driver';

// Drives the RENDERED-mode markdown editor end to end: create a markdown tile,
// type source in raw mode, flip to rendered, then edit through the canvas
// caret. The keystroke decisions are unit-tested in client/markdown (EditKey);
// these specs prove the wiring across the whole stack — window keydown →
// editRenderedKey → EditKey → content store → debounced save → durable body —
// and the two behaviors a unit test cannot see from one side of the seam:
// where keystrokes land by default, and where a canvas click puts the caret.

// createAndDescendMarkdown drops a markdown tile at the focused pane's center
// cell, descends into it (raw-text mode for a fresh tile), and returns the
// created tile's id.
async function createAndDescendMarkdown(gw: GridwellDriver): Promise<string> {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();
  await gw.descendCell(cx, cy);
  return created.id;
}

// Enter in rendered mode makes a PARAGRAPH: the persisted source gains a
// blank line ("\n\n"), not a soft break that renders as a space. This is the
// core behavior contract of the rendered editor.
test('rendered-mode Enter persists a paragraph break', async ({ gw }) => {
  const tileID = await createAndDescendMarkdown(gw);

  // Seed source through the raw editor, then flip to rendered.
  await gw.typeText('para one');
  await gw.toggleTextMode();

  // No caret is placed yet, so the first keystroke lands at the end of the
  // source; Enter must then open a new paragraph.
  await gw.typeText('x');
  await gw.pressKey('Enter');
  await gw.typeText('y');

  // Ascend (right-click the corner circle), flushing the rendered-mode edit.
  await gw.rightClickPlus();

  await expect
    .poll(async () => gw.getTileContent(tileID), { timeout: 10_000 })
    .toBe('para onex\n\ny');
});

// Repeated Enter is idempotent: blank lines never accumulate invisibly in the
// source (markdown would render three newlines the same as two, so anything
// extra is phantom state the user cannot see).
test('rendered-mode Enter does not accumulate blank lines', async ({ gw }) => {
  const tileID = await createAndDescendMarkdown(gw);

  await gw.typeText('top');
  await gw.toggleTextMode();
  await gw.pressKey('Enter');
  await gw.pressKey('Enter');
  await gw.pressKey('Enter');
  await gw.typeText('bottom');
  await gw.rightClickPlus();

  await expect
    .poll(async () => gw.getTileContent(tileID), { timeout: 10_000 })
    .toBe('top\n\nbottom');
});

// A canvas click places the caret at a real source offset and typing splices
// exactly there — the click → caret → edit wiring over the rendered layout.
test('clicking rendered text places the caret where typing lands', async ({ gw }) => {
  const tileID = await createAndDescendMarkdown(gw);

  const seed = 'hello world';
  await gw.typeText(seed);
  await gw.toggleTextMode();

  // Click near the start of the first rendered line.
  const box = await gw.textInnerBox();
  expect(box, 'descended file reports an inner box').toBeTruthy();
  await gw.clickScreen(box!.x + 10, box!.y + 10);

  const caret = await gw.renderedCaret();
  expect(caret.has, 'click placed a caret').toBe(true);
  expect(caret.offset).toBeGreaterThanOrEqual(0);
  expect(caret.offset).toBeLessThanOrEqual(seed.length);

  await gw.typeText('Z');
  await gw.rightClickPlus();

  const want = seed.slice(0, caret.offset) + 'Z' + seed.slice(caret.offset);
  await expect
    .poll(async () => gw.getTileContent(tileID), { timeout: 10_000 })
    .toBe(want);
});

// Issue #140: the raw→rendered toggle FLUSHES the textarea (one UpdateText)
// and the first rendered keystroke schedules ANOTHER save. Both used to claim
// the tile version they saw in the cache, so while the toggle's save was in
// flight the keystroke's save claimed the same version, lost the conflict,
// and the reconcile reverted the typed character — the intermittent
// "para onex → para one" flake. The saves now run through a per-tile serial
// queue that reads the version at send time. Holding every WriteContent at the
// network for longer than the save debounce (600ms) turns that former race
// into a deterministic sequence: this spec fails on the pre-queue code every
// run, not one run in twenty.
test('a keystroke typed right after the mode toggle survives a slow save', async ({
  gw,
  window,
}) => {
  const tileID = await createAndDescendMarkdown(gw);
  await gw.typeText('para one');

  await window.route('**/gridwell.v1.Gridwell/WriteContent', async (r: any) => {
    await new Promise((res) => setTimeout(res, 900));
    await r.continue();
  });

  await gw.toggleTextMode(); // flush save — held at the network
  await gw.typeText('x'); // debounced save fires while the flush is in flight
  await gw.rightClickPlus(); // ascend — its flush rides the same queue

  await expect
    .poll(async () => gw.getTileContent(tileID), { timeout: 20_000 })
    .toBe('para onex');
  await window.unroute('**/gridwell.v1.Gridwell/WriteContent');
});
