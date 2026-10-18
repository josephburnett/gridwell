import { test, expect } from './fixtures';

// The content-zoom chord inside an ephemeral visit must zoom and must not
// write: the row lives in the scratch grid and ascent deletes it, so a durable
// framing write marks a row nobody will see again. possiblyEphemeral
// (client/wasm/ephemeral.go) states the contract.
//
// The chord goes in at the window keydown, the cell grid comes back out of
// xterm, and the write's absence is counted at persistPosts and confirmed
// against the server's row. Browser mode, because a shell is the ephemeral
// visit every host has.

// The live terminal's cell width in screen pixels, from two adjacent cell
// centers. A coarser grid is the observable for a zoom that took effect.
const cellPx = async (window: any): Promise<number> => {
  const [a, b] = await window.evaluate(() => [
    (window as any).__gridwellTest.shellCellPx(0, 0),
    (window as any).__gridwellTest.shellCellPx(1, 0),
  ]);
  if (!a || !b) return 0;
  return b.x - a.x;
};

const zoomPosts = (window: any): Promise<number> =>
  window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetContentZoom ?? 0));

test('Ctrl+= in an ephemeral shell zooms live and persists nothing', async ({ window, gw }) => {
  const scratchGridID = (await gw.plugins()).find((l) => l.kind === 'home')!.scratchGridID;
  expect(scratchGridID, 'the home node advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('home');

  // A click, not a drag, creates an ephemeral shell off-grid and descends.
  await gw.clickMenuSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 20_000 }).not.toBe('');
  const scratch = await gw.getGrid(scratchGridID);
  const shells = (scratch.tiles ?? []).filter((t: any) => t.kind === 'shell');
  expect(shells, 'the visit landed in the scratch grid, so it is ephemeral').toHaveLength(1);

  // A live terminal with a measurable cell grid, and no zoom write yet.
  await expect.poll(() => cellPx(window), { timeout: 20_000 }).toBeGreaterThan(0);
  const base = await cellPx(window);
  expect(await zoomPosts(window), 'no zoom write before the chord').toBe(0);

  // 13px base font to 17px, so the cell grid must get visibly coarser.
  for (let i = 0; i < 3; i++) {
    await window.keyboard.press('Control+=');
    await gw.waitIdle();
  }
  await expect
    .poll(() => cellPx(window), { timeout: 15_000 })
    .toBeGreaterThan(base * 1.1);

  // Both halves of "nothing was written": no post, and no content_zoom.
  expect(await zoomPosts(window), 'no SetContentZoom about a row ascent deletes').toBe(0);
  const after = await gw.getGrid(scratchGridID);
  const shell = (after.tiles ?? []).find((t: any) => t.kind === 'shell')!;
  expect(Number(shell.contentZoom ?? 0), 'the ephemeral row is unmarked').toBe(0);

  // The ascent takes the row with it, and nothing surfaces on the way.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => (await gw.getGrid(scratchGridID)).tiles?.length ?? 0, { timeout: 15_000 })
    .toBe(0);
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the zoomed ephemeral visit').toHaveLength(0);
});
