import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Switching between split panes that both contain text tiles must not blank a
// pane or render its preview at the wrong size. No pane's state may decide
// another pane's paint. markdown.PreviewFrameOf is given stored framing and
// no pane's focus at all, so a sibling's inner width can never become the
// layout width. textedit.CanvasHiddenByOverlay is asked per pane, with that
// pane's own focus, so a pane editing the tile suppresses only its own canvas.
// The spec checks that the textarea overlay covers exactly the focused
// descended pane, that tiles survive each pane's rendered cache through focus
// switches, and that typed content reaches the server.
//
// Geometry: splitFocusedPaneVertical clones the focused pane's grid view, so
// both panes show the same grid at once. A vertical split halves pane width, so
// the tiles here are offset vertically; a horizontal offset can land outside the
// half-width pane and the click hits the sibling. focusPane clicks the
// pane-center pixel, so the offset cells keep that pixel clear and a focus click
// never descends.

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

  // One cell below center: visible in a half-width pane and clear of the
  // pane-center pixel focusPane clicks.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy + 1);
  const tile = tileAt(await gw.getGrid(grid), 'text', cx, cy + 1)!;
  expect(tile, 'text tile created').toBeTruthy();

  const [left, right] = await splitWithBothPanesOnGrid(gw);

  await gw.focusPane(left);
  await gw.descendCell(cx, cy + 1);

  // A freshly created tile is empty, so hasContent stays false until the user
  // types. Only the binding is asserted here.
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

  // Typing flips textareaReady, which textedit.CanvasHiddenByOverlay reads.
  const marker = 'e2e-split-text';
  await gw.typeText(marker);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // The right pane is on the same grid and shows the tile as a preview.
  await gw.focusPane(right);
  await gw.waitIdle();

  // The right pane has no text descent, so the overlay is gone and the canvas
  // is free to paint the preview.
  const taOnRight = await gw.textareaInfo();
  expect(taOnRight, 'overlay gone when focused pane has no text descent').toBeNull();

  const rightPane = (await gw.panes()).find((p) => p.id === right.id)!;
  expect(rightPane.tileIds, 'text tile still in right pane cache').toContain(tile.id);

  // The left pane is still descended into the text tile. While unfocused it was
  // canvas-painted, so the center click hits the canvas.
  await gw.focusPane(left);
  await gw.waitIdle();

  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === left.id && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // Ascending flushes the edit.
  await gw.ascendViaCrumb();

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

  // Flanking the center vertically: visible in half-width panes, with the
  // pane-center pixel left clear for focus clicks.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy - 1);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy + 1);
  const t1 = tileAt(await gw.getGrid(grid), 'text', cx, cy - 1)!;
  const t2 = tileAt(await gw.getGrid(grid), 'text', cx, cy + 1)!;
  expect(t1, 'first text tile created').toBeTruthy();
  expect(t2, 'second text tile created').toBeTruthy();

  const [leftPane, rightPane] = await splitWithBothPanesOnGrid(gw);

  await gw.focusPane(leftPane);
  await gw.descendCell(cx, cy - 1);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === leftPane.id;
  }, { timeout: 10_000 }).toBe(true);

  const marker1 = 'left-pane-text';
  await gw.typeText(marker1);

  // The left pane keeps its text descent. It is no longer focused, so the canvas
  // paints it rather than the overlay. A cross-pane hide decision would blank
  // that pane.
  await gw.focusPane(rightPane);
  await gw.descendCell(cx, cy + 1);
  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === rightPane.id;
  }, { timeout: 10_000 }).toBe(true);

  const marker2 = 'right-pane-text';
  await gw.typeText(marker2);

  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === rightPane.id && ta.hasContent;
  }, { timeout: 10_000 }).toBe(true);

  // The left pane is still descended into t1 in text mode, so the overlay moves
  // back to it.
  await gw.focusPane(leftPane);
  await gw.waitIdle();

  await expect.poll(async () => {
    const ta = await gw.textareaInfo();
    return ta != null && ta.paneID === leftPane.id && ta.tileID === t1.id;
  }, { timeout: 10_000 }).toBe(true);

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
