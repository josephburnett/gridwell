import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Preview equals descent target equals ascent return, and nothing the user did
// not touch changes. framing-roundtrip.spec.ts locks the viewport half; this
// locks the preview half through the read-only previewSigs hook, a per-tile
// signature over exactly the fields the preview renderer reads: the tile row
// and the door's cached child grid.

// sigs reads the preview signatures of the focused pane's grid.
async function sigs(window: any): Promise<Record<string, string>> {
  return window.evaluate(() => (window as any).__gridwellTest.previewSigs());
}

test('a descend/ascend round trip leaves every preview byte-identical; a reframe changes only the reframed door', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;

  // A door with content, and an unrelated markdown tile beside it.
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx + 1, cy);
  const door = tileAt(await gw.getGrid(a.gridID), 'door', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.openMenu();
  const inner = await gw.focused();
  await gw.dragCreate('markdown', Math.round(inner.cx), Math.round(inner.cy) - 1);
  await gw.middleClickCell(Math.round(inner.cx), Math.round(inner.cy) + 1);

  // Previews fetch child grids, so warm the caches before the baseline.
  await gw.waitIdle();
  const before = await sigs(window);
  expect(Object.keys(before).length, 'baseline has both tiles').toBe(2);
  expect(before[door.id], 'door signature includes its child grid').toContain('|');

  // Reading never mutates, so a round trip with no reframe leaves every
  // signature byte-identical.
  await gw.descendCell(cx, cy);
  {
    const f = await gw.focused();
    await gw.middleClickCell(Math.round(f.cx), Math.round(f.cy) + 1);
  }
  await gw.waitIdle();
  expect(await sigs(window), 'pure round trip changed a preview').toEqual(before);

  // A zoom is a framing change, so the door's preview updates to it: preview
  // equals ascent return. Everything the user did not touch stays
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
    if (id === door.id) continue;
    expect(after[id], `unrelated tile ${id} changed during a sibling reframe`).toBe(before[id]);
  }
  expect(after[door.id], 'the reframed door shows its NEW framing (ascent wrote it back)').not.toBe(before[door.id]);

  // The framing writeback is not a content edit, so the version must not bump.
  const doorAfter = tileAt(await gw.getGrid(a.gridID), 'door', cx, cy)!;
  expect(Number(doorAfter.version ?? 0), 'reframe bumped the version').toBe(Number(door.version ?? 0));
});
