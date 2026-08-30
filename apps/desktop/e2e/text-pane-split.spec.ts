import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Switching between split panes that both contain text tiles must not blank a
// pane or render its preview at the wrong size. Two rules keep that true, and
// both come down to never letting one pane's state decide another's paint:
//
//   The preview always passes focused=false to PreviewScaleScroll and uses
//   stored framing only. Looking up whether some pane is focused on the file
//   would let a sibling pane's inner width become the layout width.
//
//   The preview path never suppresses the canvas. The same cross-pane lookup in
//   hideForTextarea would suppress paint in every pane showing the tile whenever
//   another pane was editing it, not just the pane the textarea covers.
//
// So this verifies:
//   1. The textarea overlay covers exactly the focused descended pane, never a
//      sibling preview pane, asserted through textareaInfo().
//   2. Tiles survive in each pane's rendered cache through focus switches.
//   3. Typed content persists to the server through the full round trip.
//
// Geometry notes:
//   - splitFocusedPaneVertical clones the focused pane's grid view, so both
//     panes show the same grid immediately.
//   - A vertical split halves pane width, so tiles the spec descends into are
//     offset vertically from the viewport center. A horizontal offset can land
//     outside the half-width pane and the click hits the sibling pane.
//   - focusPane clicks the pane-center pixel, so the offset cells keep the
//     center clear and a focus click never descends.

// splitWithBothPanesOnGrid splits the focused grid pane. The new pane is a clone
// of the same grid view, so both panes sit on the root grid immediately. Returns
// the left and right pane infos.
async function splitWithBothPanesOnGrid(gw: any): Promise<[any, any]> {
  await gw.splitFocusedPaneVertical();
  const after = await gw.panes();
  expect(after.length, 'two panes after split').toBe(2);
  const sorted = after.slice().sort((a: any, b: any) => a.x - b.x);
  expect(sorted[0].gridID, 'both panes on the same grid').toBe(sorted[1].gridID);
  return [sorted[0], sorted[1]];
}

test('split pane text tiles: overlay covers only focused pane, not preview', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f0 = await gw.focused();
  const grid = f0.gridID;
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);

  // Create a text tile one cell below center: visible in a half-width pane and
  // clear of the pane-center pixel focusPane clicks.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy + 1);
  const tile = tileAt(await gw.getGrid(grid), 'text', cx, cy + 1)!;
  expect(tile, 'text tile created').toBeTruthy();

  const [left, right] = await splitWithBothPanesOnGrid(gw);

  // Focus the left pane and descend into the text tile, entering raw-text mode.
  await gw.focusPane(left);
  await gw.descendCell(cx, cy + 1);

  // After the descent the overlay must exist and be bound to this pane. A freshly
  // created tile is empty, so hasContent is legitimately false until the user
  // types; only the binding is asserted here.
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

  // Type a marker. Typing must flip textareaReady, which the canvas hide decision
  // in textedit.CanvasHiddenByOverlay depends on.
  const marker = 'e2e-split-text';
  await gw.typeText(marker);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // Switch focus to the right pane, which is on the same grid and shows the tile
  // as a preview.
  await gw.focusPane(right);
  await gw.waitIdle();

  // With the right pane focused, and no text descent there, the overlay must be
  // gone: it never covers a preview pane. Its absence is what leaves the canvas
  // free to paint the preview.
  const taOnRight = await gw.textareaInfo();
  expect(taOnRight, 'overlay gone when focused pane has no text descent').toBeNull();

  // The text tile must still be in the right pane's rendered cache, so the tile
  // was never lost.
  const rightPane = (await gw.panes()).find((p) => p.id === right.id)!;
  expect(rightPane.tileIds, 'text tile still in right pane cache').toContain(tile.id);

  // Switch focus back to the left pane, still descended into the text tile. While
  // unfocused it was canvas-painted, so the center click hits the canvas.
  await gw.focusPane(left);
  await gw.waitIdle();

  // The overlay must be back on the left pane, with content.
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === left.id && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // Ascend from the text descent to flush the edit.
  await gw.ascendViaCrumb();

  // Server must have the typed marker.
  await expect
    .poll(async () => gw.getTileContent(tile.id), { timeout: 10_000 })
    .toContain(marker);
});

test('split pane: focus switch between two text descents preserves both tiles', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f0 = await gw.focused();
  const grid = f0.gridID;
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);

  // Create two text tiles flanking the center vertically: visible in half-width
  // panes, with the pane-center pixel left clear for focus clicks.
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

  // Switch to the right pane and descend into t2. The left pane keeps its text
  // descent, and since it is no longer focused the canvas paints it rather than
  // the overlay: the path a cross-pane hide decision would blank.
  await gw.focusPane(rightPane);
  await gw.descendCell(cx, cy + 1);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === rightPane.id;
  }, { timeout: 10_000 }).toBe(true);

  // Type into t2.
  const marker2 = 'right-pane-text';
  await gw.typeText(marker2);

  // The overlay must now be on the right pane, with content.
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === rightPane.id && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // Switch focus back to the left pane. It is still descended into t1 in text
  // mode, so the overlay moves to it.
  await gw.focusPane(leftPane);
  await gw.waitIdle();

  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === leftPane.id && ta.tileID === t1.id;
  }, { timeout: 10_000 }).toBe(true);

  // Ascend both panes and check server truth.
  await gw.ascendViaCrumb(); // ascend left pane
  await gw.focusPane(rightPane);
  await gw.waitIdle();
  await gw.ascendViaCrumb(); // ascend right pane

  await expect
    .poll(async () => gw.getTileContent(t1.id), { timeout: 10_000 })
    .toContain(marker1);
  await expect
    .poll(async () => gw.getTileContent(t2.id), { timeout: 10_000 })
    .toContain(marker2);
});
