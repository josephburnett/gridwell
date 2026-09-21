import { test, expect } from './fixtures';
import { deleteTile, tileAt } from './oracle';

// A descent whose row is gone is a verdict, not a dead namespace: the id still
// routes, the node still answers, and what it answers is "no such tile"
// (client/deadref owns the other case, which is silent by decision). Without a
// notice the pane draws an empty content box named "unnamed" and the strip says
// nothing, which reads as "it just disappeared".
//
// Deleting once only moves the row to the trash, where it still reads, so the
// pane follows it, keeps its faces, and stays quiet. The second delete is
// inside the trash, which bypasses it, and that is the verdict.
//
// The by-id read is in the wasm shim, which `make check` compiles and never
// runs; the seam crossed here is a real GetTile verdict reaching the real strip.

test('a descent whose row is deleted under it says so on the strip', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(doc, 'markdown tile created').toBeTruthy();

  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  expect((await gw.focused()).textFocus, 'the pane is in the doc').toBe(doc.id);

  const notice = async () =>
    (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices.find(
      (n: any) => n.source === `tile:${doc.id}`,
    ) ?? null;
  expect(await notice(), 'nothing is said while the row is there').toBeNull();

  // A foreign writer deletes the row out from under the descent: another
  // device, or this one in another pane. The row lands in the trash, where the
  // by-id read still finds it, so the pane keeps showing the doc.
  await deleteTile(gw.origin, doc.id);
  await expect
    .poll(async () => (await gw.focused()).tileIds.includes(doc.id), { timeout: 15_000 })
    .toBe(false);
  await window.waitForTimeout(2_000);
  expect(await notice(), 'a trashed row still reads, so nothing is said').toBeNull();

  // The faces stay with it. Every text overlay gates on the one resolved row,
  // so a row the pane's own grid no longer holds cannot leave the doc showing
  // rendered with no toggle and no editor.
  await expect(window.locator('#gw-text-toggle'), 'the toggle follows the row').toBeVisible();
  await expect(window.locator('#gw-text-editor'), 'the editor follows the row').toBeVisible();

  // Deleting it out of the trash is the verdict: the id routes, the node
  // answers, and the answer is that there is no such tile.
  await deleteTile(gw.origin, doc.id);
  await expect
    .poll(async () => (await notice())?.severity ?? null, {
      message: 'the verdict on the vanished row reaches the strip',
      timeout: 15_000,
    })
    .toBe('error');
});
