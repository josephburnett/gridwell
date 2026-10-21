import { test, expect } from './fixtures';

// The content-zoom chord inside an ephemeral visit zooms and writes, like a
// placed tile's: an ephemeral row differs only in that the ascent deletes it,
// and that delete follows the zoom write on the row's chain.
//
// The chord goes in at the window keydown, the cell grid comes back out of
// xterm, and the write is counted at persistPosts and confirmed against the
// server's row. Browser mode, because a shell is the ephemeral visit every
// host has.

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

test('Ctrl+= in an ephemeral shell zooms live and persists like a placed tile', async ({ window, gw }) => {
  const scratchGridID = (await gw.plugins()).find((l) => l.label === 'home')!.scratchGridID;
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

  // Both halves of the write: the posts, and the row's content_zoom.
  expect(await zoomPosts(window), 'one SetContentZoom per chord').toBe(3);
  await expect
    .poll(async () => {
      const after = await gw.getGrid(scratchGridID);
      return Number((after.tiles ?? []).find((t: any) => t.kind === 'shell')?.contentZoom ?? 0);
    }, { timeout: 10_000 })
    .toBeCloseTo(1.331, 2);

  // The ascent takes the row with it, and nothing surfaces on the way.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => (await gw.getGrid(scratchGridID)).tiles?.length ?? 0, { timeout: 15_000 })
    .toBe(0);
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the zoomed ephemeral visit').toHaveLength(0);
});
