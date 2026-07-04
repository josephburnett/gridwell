import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Regression guard for issue #35: switching between split panes that both
// contain text tiles must not blank a pane or render its preview at the wrong
// size. Two mechanisms were fixed:
//
//   A (wrong-size preview): drawMarkdownNode called paneFocusedOnFile and used
//   a sibling pane's inner width as the layout width. After the fix it always
//   passes focused=false to PreviewScaleScroll, using stored framing only.
//
//   B (blank pane): the same cross-pane lookup caused hideForTextarea to
//   suppress canvas paint in EVERY pane showing the tile when another pane was
//   editing it in text mode — not just the pane the textarea actually covers.
//   After the fix the preview path never suppresses canvas.
//
// The e2e verifies:
//   1. The textarea overlay covers exactly the focused descended pane (not a
//      sibling preview pane) — asserted via textareaInfo().
//   2. Tiles are preserved in each pane's rendered cache through focus switches.
//   3. Typed content persists to the server through the full round trip.

test('split pane text tiles: overlay covers only focused pane, not preview', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const f0 = await gw.focused();
  const grid = f0.gridID;
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);

  // Create a text tile on the grid.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const tile = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(tile, 'text tile created').toBeTruthy();

  // Split into two panes. Both now show the same grid with the text tile.
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length, 'two panes after split').toBe(2);

  // Focus the LEFT pane and descend into the text tile (enters raw-text mode).
  const left = panes.slice().sort((a, b) => a.x - b.x)[0];
  await gw.focusPane(left);
  await gw.descendCell(cx, cy);

  // After descent the focused pane is in text mode. The textarea overlay must
  // cover the focused pane and report that it has content (textareaReady).
  // This may not be immediate if the blob is loading — poll.
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  const taAfterDescent = await gw.textareaInfo();
  expect(taAfterDescent, 'textarea binding reported').not.toBeNull();
  expect(taAfterDescent!.paneID, 'overlay on the focused (descended) pane').toBe(
    (await gw.focused()).id,
  );
  expect(taAfterDescent!.tileID, 'overlay bound to the text tile').toBe(tile.id);

  // Type a marker into the focused pane.
  const marker = 'e2e-split-text';
  await gw.typeText(marker);

  // Switch focus to the RIGHT pane (which shows the parent grid, T1 as preview).
  const right = panes.slice().sort((a, b) => b.x - a.x)[0];
  await gw.focusPane(right);
  await gw.waitIdle();

  // After focusing the right pane (no text descent), the textarea overlay must
  // be gone — it never covers a preview pane. This is the mechanism B assertion:
  // the overlay not being here means canvas was free to paint the preview.
  const taOnRight = await gw.textareaInfo();
  expect(taOnRight, 'overlay gone when focused pane has no text descent').toBeNull();

  // The text tile must still be in the right pane's rendered cache (not blanked
  // out of cache, just possibly hidden on canvas by the old bug — we verify
  // the cache so we know the tile was never lost).
  const rightPane = (await gw.panes()).find((p) => p.id === right.id)!;
  expect(rightPane.tileIds, 'text tile still in right pane cache').toContain(tile.id);

  // Switch focus back to the left pane (still descended into the text tile).
  const leftPane = (await gw.panes()).find((p) => p.id === left.id)!;
  await gw.focusPane(leftPane);
  await gw.waitIdle();

  // Overlay must be back on the left pane with content.
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === left.id && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // Ascend from the text descent to flush the edit.
  await gw.rightClickPlus();

  // Server must have the typed marker.
  await expect
    .poll(async () => gw.getTileContent(tile.id), { timeout: 10_000 })
    .toContain(marker);
});

test('split pane: focus switch between two text descents preserves both tiles', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const f0 = await gw.focused();
  const grid = f0.gridID;
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);

  // Create two text tiles side-by-side.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 1, cy);
  const t1 = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  const t2 = tileAt(await gw.getGrid(grid), 'text', cx + 1, cy)!;
  expect(t1, 'first text tile created').toBeTruthy();
  expect(t2, 'second text tile created').toBeTruthy();

  // Split and descend into each tile in its own pane.
  await gw.splitFocusedPaneVertical();
  const [leftPane, rightPane] = (await gw.panes()).slice().sort((a, b) => a.x - b.x);

  // Descend left pane into t1.
  await gw.focusPane(leftPane);
  await gw.descendCell(cx, cy);

  // Type into t1.
  const marker1 = 'left-pane-text';
  await gw.typeText(marker1);

  // Switch to right pane, descend into t2.
  await gw.focusPane(rightPane);
  await gw.descendCell(cx + 1, cy);

  // Type into t2.
  const marker2 = 'right-pane-text';
  await gw.typeText(marker2);

  // The overlay must now be on the right pane (t2), not the left (t1).
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === rightPane.id && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // Switch focus back to the left pane. The left pane is still descended into t1
  // (text mode); the overlay moves to it.
  await gw.focusPane(leftPane);
  await gw.waitIdle();

  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === leftPane.id && ta.tileID === t1.id;
  }, { timeout: 10_000 }).toBe(true);

  // Ascend both panes and check server truth.
  await gw.rightClickPlus(); // ascend left pane
  await gw.focusPane(rightPane);
  await gw.waitIdle();
  await gw.rightClickPlus(); // ascend right pane

  await expect
    .poll(async () => gw.getTileContent(t1.id), { timeout: 10_000 })
    .toContain(marker1);
  await expect
    .poll(async () => gw.getTileContent(t2.id), { timeout: 10_000 })
    .toContain(marker2);
});
