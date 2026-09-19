import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Freezing is a screenshot, and a shell has it too: the bar circle over a live
// shell freezes the terminal's face onto the tile, the standing intent keeps
// the next descent from reattaching, and a clone taken while it is frozen
// keeps that picture after the original reconnects. This drives the seam the
// unit tests cannot: barslot.Decide's two shell arms, the capture writeback,
// the SetTile url_frozen arm on a shell row, the stored column, clone, and
// shellconn.DecideAutoLive. A freeze never kills the tmux session, which the
// reconnect proves by finding the shell it left.

test('freezing a live shell keeps its face; the clone keeps it after the original reconnects', async ({
  gw,
  window,
}) => {
  const slot = () => window.evaluate(() => (window as any).__gridwellTest.barSlot());
  const shellText = () => window.evaluate(() => (window as any).__gridwellTest.shellText());
  // The circle's own center, the same geometry the click handler hit-tests.
  const clickSlot = async () => {
    const pal = await gw.palette();
    await gw.clickScreen(pal.plusX, pal.plusY);
  };

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const grid = f.gridID;
  const shellAt = async (x: number, y: number) => tileAt(await gw.getGrid(grid), 'shell', x, y);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();

  // Something on the terminal, so the captured face has glyphs of its own.
  await window.keyboard.type('printf gw-frozen-face');
  await window.keyboard.press('Enter');

  // The circle over a live shell is the freeze.
  await expect.poll(slot, { timeout: 10_000 }).toBe('freeze');
  await clickSlot();

  // The attachment ends, the capture lands, and the intent is stored.
  await expect.poll(shellText, { timeout: 10_000 }).toBe('');
  await expect
    .poll(async () => Boolean((await shellAt(cx, cy))?.urlFrozen), { timeout: 10_000 })
    .toBe(true);
  const frozen = (await shellAt(cx, cy))!;
  expect(
    Number(frozen.previewBlobId ?? 0),
    'the freeze captured the terminal face',
  ).toBeGreaterThan(0);

  // Re-descending shows that face and reattaches nothing.
  await gw.middleClickCell(cx, cy); // ascend
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).not.toBe('');
  await window.waitForTimeout(1_500); // give a wrong auto-live time to fire
  expect(await shellText(), 'a frozen shell does not reattach on descent').toBe('');
  expect(Boolean((await shellAt(cx, cy))?.urlFrozen), 'and it stays frozen').toBe(true);

  // A clone taken while frozen is the screenshot copy.
  await gw.middleClickCell(cx, cy); // ascend to the grid to drag
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.cloneTileCell(cx, cy, cx + 2, cy);
  const copy = await shellAt(cx + 2, cy);
  expect(copy, 'the clone landed').toBeTruthy();
  expect(copy!.id, 'the clone is its own tile').not.toBe(frozen.id);
  expect(Boolean(copy!.urlFrozen), 'the clone carries the freeze').toBe(true);
  expect(copy!.previewBlobId, 'and the same captured face').toBe(frozen.previewBlobId);

  // Reconnecting the original finds the session the freeze left running, and
  // going live is what clears the standing intent.
  await gw.descendCell(cx, cy);
  await expect.poll(slot, { timeout: 15_000 }).toBe('golive');
  await clickSlot();
  await gw.shellAttached();
  await expect
    .poll(async () => Boolean((await shellAt(cx, cy))?.urlFrozen), { timeout: 10_000 })
    .toBe(false);

  // The copy kept the screenshot: nothing the original did reached it.
  const after = await shellAt(cx + 2, cy);
  expect(Boolean(after!.urlFrozen), 'the clone is still frozen').toBe(true);
  expect(after!.previewBlobId, 'with the face it was cloned with').toBe(frozen.previewBlobId);

  await gw.ascendViaCrumb(); // teardown: a live shell's overlay swallows a middle click
});
