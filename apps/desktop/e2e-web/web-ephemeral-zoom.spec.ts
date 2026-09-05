import { test, expect } from './fixtures';

// The content-zoom chord inside an EPHEMERAL visit: it must zoom, and it must
// not write. An ephemeral shell's row lives in the scratch grid and ascent
// deletes it, so a durable framing write about it leaves a mark on a row
// nobody asked for and nobody will ever see again — the contract stated on
// possiblyEphemeral (client/wasm/input.go), which every durable write about a
// descent reads. applyContentZoom was the one that did not.
//
// The seam this crosses: the chord goes in at the window keydown, the live
// terminal's cell grid comes back out of xterm, and the write — or its
// absence — is counted at the settle-persist dispatcher (persistPosts) and
// confirmed against the server's own row through the oracle. A unit test on
// either side sees none of that composition.
//
// Browser mode, because a shell is the ephemeral visit every host has: shells
// ride the web door, so this is the whole chain with no Electron anywhere.

// cellPx is the live terminal's cell width in screen pixels, derived from two
// adjacent cell centers. It is the observable for "the zoom took effect": the
// font grew, the fit re-ran, and the cell grid is coarser. 0 when no shell.
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

  // Click the shell swatch, rather than dragging: an ephemeral shell, created
  // off-grid in the scratch grid and descended into.
  await gw.clickPaletteSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 20_000 }).not.toBe('');
  const scratch = await gw.getGrid(scratchGridID);
  const shells = (scratch.tiles ?? []).filter((t: any) => t.kind === 'shell');
  expect(shells, 'the visit landed in the scratch grid, so it is ephemeral').toHaveLength(1);

  // A live terminal with a measurable cell grid, and no zoom write yet.
  await expect.poll(() => cellPx(window), { timeout: 20_000 }).toBeGreaterThan(0);
  const base = await cellPx(window);
  expect(await zoomPosts(window), 'no zoom write before the chord').toBe(0);

  // Three steps of Ctrl+=: 13px base font to 17px, so the cell grid must get
  // visibly coarser. Settle between presses, as the desktop zoom spec does.
  for (let i = 0; i < 3; i++) {
    await window.keyboard.press('Control+=');
    await gw.waitIdle();
  }
  await expect
    .poll(() => cellPx(window), { timeout: 15_000 })
    .toBeGreaterThan(base * 1.1);

  // ...and nothing was written. Both halves: the dispatcher never posted, and
  // the server's row still carries no content_zoom.
  expect(await zoomPosts(window), 'no SetContentZoom about a row ascent deletes').toBe(0);
  const after = await gw.getGrid(scratchGridID);
  const shell = (after.tiles ?? []).find((t: any) => t.kind === 'shell')!;
  expect(Number(shell.contentZoom ?? 0), 'the ephemeral row is unmarked').toBe(0);

  // Ascend: the row goes, and nothing surfaced on the way.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => (await gw.getGrid(scratchGridID)).tiles?.length ?? 0, { timeout: 15_000 })
    .toBe(0);
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the zoomed ephemeral visit').toHaveLength(0);
});
