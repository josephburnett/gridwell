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
//
// Geometry notes (learned the hard way):
//   - splitFocusedPaneVertical clones the focused pane's grid view (issue
//     #27): both panes show the same grid immediately.
//   - A vertical split halves pane WIDTH, so tiles the spec descends into are
//     offset VERTICALLY (cy±1) from the viewport center — a cx±1 offset can land
//     outside the half-width pane and the click hits the sibling pane instead.
//   - focusPane clicks the pane-center pixel (cell (0,0) area); the offset cells
//     keep the center clear so a focus click never descends.

// splitWithBothPanesOnGrid splits the focused (grid) pane; the new pane is a
// clone of the same grid view (issue #27), so both panes are on the plugin's
// root grid immediately. Returns [left, right] pane infos.
async function splitWithBothPanesOnGrid(gw: any): Promise<[any, any]> {
  await gw.splitFocusedPaneVertical();
  const after = await gw.panes();
  expect(after.length, 'two panes after split').toBe(2);
  const sorted = after.slice().sort((a: any, b: any) => a.x - b.x);
  expect(sorted[0].gridID, 'both panes on the same grid').toBe(sorted[1].gridID);
  return [sorted[0], sorted[1]];
}

test('split pane text tiles: overlay covers only focused pane, not preview', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const f0 = await gw.focused();
  const grid = f0.gridID;
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);

  // Create a text tile one cell BELOW center: visible in a half-width pane,
  // clear of the pane-center pixel that focusPane clicks.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy + 1);
  const tile = tileAt(await gw.getGrid(grid), 'text', cx, cy + 1)!;
  expect(tile, 'text tile created').toBeTruthy();

  const [left, right] = await splitWithBothPanesOnGrid(gw);

  // Focus the LEFT pane and descend into the text tile (enters raw-text mode).
  await gw.focusPane(left);
  await gw.descendCell(cx, cy + 1);

  // After descent the overlay must be present and bound to this pane. A freshly
  // created tile is EMPTY, so hasContent (textareaReady) is legitimately false
  // until the user types — only the binding is asserted here.
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null;
  }, { timeout: 10_000 }).toBe(true);

  const taAfterDescent = await gw.textareaInfo();
  expect(taAfterDescent, 'textarea binding reported').not.toBeNull();
  expect(taAfterDescent!.paneID, 'overlay on the focused (descended) pane').toBe(
    (await gw.focused()).id,
  );
  expect(taAfterDescent!.tileID, 'overlay bound to the text tile').toBe(tile.id);

  // Type a marker. Typing must flip textareaReady — the canvas hide decision
  // (textedit.CanvasHiddenByOverlay) depends on it.
  const marker = 'e2e-split-text';
  await gw.typeText(marker);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // Switch focus to the RIGHT pane (same grid, shows the tile as a preview).
  await gw.focusPane(right);
  await gw.waitIdle();

  // With the right pane focused (no text descent there), the overlay must be
  // gone — it never covers a preview pane. This is the mechanism B assertion:
  // the overlay not being here means canvas was free to paint the preview.
  const taOnRight = await gw.textareaInfo();
  expect(taOnRight, 'overlay gone when focused pane has no text descent').toBeNull();

  // The text tile must still be in the right pane's rendered cache (not blanked
  // out of cache — we verify the cache so we know the tile was never lost).
  const rightPane = (await gw.panes()).find((p) => p.id === right.id)!;
  expect(rightPane.tileIds, 'text tile still in right pane cache').toContain(tile.id);

  // Switch focus back to the left pane (still descended into the text tile;
  // while unfocused it was canvas-painted, so the center click hits the canvas).
  await gw.focusPane(left);
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

  // Create two text tiles flanking the center vertically (visible in half-width
  // panes; the pane-center pixel stays clear for focus clicks).
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy - 1);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy + 1);
  const t1 = tileAt(await gw.getGrid(grid), 'text', cx, cy - 1)!;
  const t2 = tileAt(await gw.getGrid(grid), 'text', cx, cy + 1)!;
  expect(t1, 'first text tile created').toBeTruthy();
  expect(t2, 'second text tile created').toBeTruthy();

  const [leftPane, rightPane] = await splitWithBothPanesOnGrid(gw);

  // Descend left pane into t1.
  await gw.focusPane(leftPane);
  await gw.descendCell(cx, cy - 1);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === leftPane.id;
  }, { timeout: 10_000 }).toBe(true);

  // Type into t1.
  const marker1 = 'left-pane-text';
  await gw.typeText(marker1);

  // Switch to right pane and descend into t2. The left pane keeps its text
  // descent; because it is no longer focused, the canvas (not the overlay)
  // paints it — the exact path mechanism B used to blank.
  await gw.focusPane(rightPane);
  await gw.descendCell(cx, cy + 1);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === rightPane.id;
  }, { timeout: 10_000 }).toBe(true);

  // Type into t2.
  const marker2 = 'right-pane-text';
  await gw.typeText(marker2);

  // The overlay must now be on the right pane with content.
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
