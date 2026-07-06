import { test, expect, GridwellDriver } from './fixtures';
import { tileAt } from './oracle';

// Embeds through the real stack (issue #6): a markdown link whose href is a
// tile id renders as a live preview tile in rendered mode (an embedHit),
// clicking it descends onto the target, editing the doc doesn't revert the
// embed to link text, and dropping a tile onto a raw-mode doc splices an
// embed link. The pure logic (client/embed) was already unit-tested; these
// cross the wasm orchestration that composes it — previously reachable by
// no gate at all.

async function embedHits(window: any): Promise<Array<{ paneId: string; href: string; tileId: string; x: number; y: number; w: number; h: number }>> {
  return window.evaluate(() => (window as any).__gridwellTest.embedHits());
}

// setup: a target markdown tile (with content) and a doc tile, side by side.
async function makeTargetAndDoc(gw: GridwellDriver) {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 1, cy);
  const target = tileAt(await gw.getGrid(f.gridID), 'text', cx + 1, cy)!;
  await gw.descendCell(cx + 1, cy);
  await gw.typeText('# the target');
  await gw.rightClickPlus(); // corner-circle ascent out of the text descent
  await gw.waitIdle();
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  return { grid: f.gridID, cx, cy, target, doc };
}

test('an embed renders as a preview, descends on click, and survives edits', async ({ gw, window }) => {
  const { cx, cy, target } = await makeTargetAndDoc(gw);

  // Author the embed in raw mode: a markdown link whose href is the target's
  // qualified id ("/uuid/id" — the single-globally-routable-id contract).
  await gw.descendCell(cx, cy);
  await gw.typeText(`intro [box](/${target.id}) outro`);
  await gw.toggleFileMode(); // rendered mode
  await gw.waitIdle();

  // The embed drew, and its href resolved to the real target tile.
  const hits = await embedHits(window);
  expect(hits.length, 'one embed rendered').toBe(1);
  expect(hits[0].tileId, 'the href resolved to the target tile').toBe(target.id);
  expect(hits[0].w, 'the embed has a preview footprint').toBeGreaterThan(0);

  // Editing the doc must not revert the embed to link text (the named
  // recurring regression): type at the caret-less end, re-check the hit.
  await gw.typeText(' more');
  await gw.waitIdle();
  const afterEdit = await embedHits(window);
  expect(afterEdit.length, 'the embed survived an edit').toBe(1);
  expect(afterEdit[0].tileId).toBe(target.id);

  // Clicking the embed descends onto the target tile.
  await window.mouse.click(hits[0].x + hits[0].w / 2, hits[0].y + hits[0].h / 2);
  await gw.waitIdle();
  const landed = await gw.focused();
  expect(landed.textFocus, 'embed click descended into the target').toBe(target.id);
});

test('dropping a tile onto a raw-mode doc splices an embed link', async ({ gw, window }) => {
  const { grid, cx, cy, target, doc } = await makeTargetAndDoc(gw);

  // Pane A: descend into the doc in RAW mode. Pane B (the split clone) shows
  // the grid, where the target tile sits.
  await gw.descendCell(cx, cy);
  await gw.typeText('before ');
  const a = await gw.focused();
  await gw.splitFocusedPaneVertical();
  const b = (await gw.panes()).find((p) => p.id !== a.id)!;
  expect(b.gridID, 'split pane shows the grid').toBe(grid);

  // Left-drag the TARGET tile from pane B onto pane A's doc: DropEmbed —
  // the doc is not a placement medium, so the drag splices a reference and
  // leaves the source in place.
  const from = await gw.cellCenter(b.id, cx + 1, cy);
  const aNow = (await gw.panes()).find((p) => p.id === a.id)!; // post-split rect (half width)
  const m = window.mouse;
  await m.move(from.x, from.y);
  await m.down();
  await m.move(from.x + 10, from.y + 10);
  await m.move(aNow.x + aNow.w / 2, aNow.y + aNow.h / 2, { steps: 10 });
  await m.up();
  await gw.waitIdle();

  // The doc body gained an embed link to the target, and the target tile
  // survived on the grid (embed-drop is clone semantics, not move).
  await expect
    .poll(async () => gw.getTileContent(doc.id), { timeout: 10_000 })
    .toContain(`/${target.id}`);
  expect(tileAt(await gw.getGrid(grid), 'text', cx + 1, cy), 'source tile still on the grid').toBeTruthy();
});
