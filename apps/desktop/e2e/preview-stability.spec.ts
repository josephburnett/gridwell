import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Preview equals descent target equals ascent return, and nothing the user did
// not touch changes. framing-roundtrip.spec.ts locks the viewport half; this
// locks the preview half through the read-only previewSigs hook, a per-tile
// signature over exactly the fields the preview renderer reads: the tile row and
// the well's cached child grid. The signatures are captured from a sibling pane
// while another pane descends, reframes, and ascends.

// sigs reads the preview signatures of the focused pane's grid.
async function sigs(window: any): Promise<Record<string, string>> {
  return window.evaluate(() => (window as any).__gridwellTest.previewSigs());
}

test('a descend/ascend round trip leaves every preview byte-identical; a reframe changes only the reframed well', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;

  // A well with content, plus an unrelated markdown tile beside it.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 1, cy);
  const well = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.openPalette();
  const inner = await gw.focused();
  await gw.dragCreate('markdown', Math.round(inner.cx), Math.round(inner.cy) - 1);
  await gw.middleClickCell(Math.round(inner.cx), Math.round(inner.cy) + 1);

  // Warm the caches, since previews fetch child grids, then capture the baseline.
  await gw.waitIdle();
  const before = await sigs(window);
  expect(Object.keys(before).length, 'baseline has both tiles').toBe(2);
  expect(before[well.id], 'well signature includes its child grid').toContain('|');

  // A round trip without reframing: descend into the well and ascend straight
  // back. Reading never mutates, so every signature must be byte-identical.
  await gw.descendCell(cx, cy);
  {
    const f = await gw.focused();
    await gw.middleClickCell(Math.round(f.cx), Math.round(f.cy) + 1);
  }
  await gw.waitIdle();
  expect(await sigs(window), 'pure round trip changed a preview').toEqual(before);

  // A round trip with a reframe: descend, zoom, which is a framing change, then
  // ascend. The well's preview updates to the new framing, because preview
  // equals ascent return; everything the user did not touch stays
  // byte-identical.
  await gw.descendCell(cx, cy);
  await gw.wheelAtFocusedCenter(-300);
  {
    const f = await gw.focused();
    await gw.middleClickCell(Math.round(f.cx), Math.round(f.cy) + 1);
  }
  await gw.waitIdle();
  const after = await sigs(window);
  for (const id of Object.keys(before)) {
    if (id === well.id) continue;
    expect(after[id], `unrelated tile ${id} changed during a sibling reframe`).toBe(before[id]);
  }
  expect(after[well.id], 'the reframed well shows its NEW framing (ascent wrote it back)').not.toBe(before[well.id]);

  // The framing writeback is not a content edit, so the version must not bump.
  const wellAfter = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  expect(Number(wellAfter.version ?? 0), 'reframe bumped the version').toBe(Number(well.version ?? 0));
});
