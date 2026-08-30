import { test, expect } from './fixtures';

// Descending into a url from the menu, on the real stack: clicking the url
// swatch rather than dragging it opens the url modal, and submitting descends
// into a live url tile created in the off-grid scratch grid. The visit must
// place no tile on the visible home grid, must go live as a native
// WebContentsView, and must leave nothing behind on ascent. Ephemeral means
// ephemeral: the ascent deletes the scratch tile, with no history record and no
// error surfaced.
//
// Most of this is server-observable through the getGrid oracle, so it does not
// depend on the page loading; the one native check is that a live view was
// created.
test('clicking the menu url swatch descends into an off-grid ephemeral visit', async ({ electronApp, window, gw }) => {
  // An empty grid comes back with no tiles field, since proto3 omits an empty
  // repeated field, so count defensively.
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  // The scratch grid id is advertised on the plugin entry.
  const local = (await gw.plugins()).find((l) => l.kind === 'home');
  expect(local, 'localdb plugin configured').toBeTruthy();
  const scratchGridID = local!.scratchGridID;
  expect(scratchGridID, 'localdb advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('home');
  const home = await gw.focused();
  const homeBefore = await gw.getGrid(home.gridID);
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);

  // Click the url swatch, rather than dragging: the ephemeral-visit modal opens.
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/ephemeral');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  // The visit goes live asynchronously, through create, descend, and open
  // stream, so poll until a native WebContentsView has been placed for it.
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // The ephemeral url landed in the off-grid scratch grid, not on home.
  const scratch = await gw.getGrid(scratchGridID);
  const scratchURLs = (scratch.tiles ?? []).filter(
    (t) => t.kind === 'url' && String(t.urlString ?? '').includes('example.com'),
  );
  expect(scratchURLs, 'one ephemeral url tile in the scratch grid').toHaveLength(1);

  const homeAfter = await gw.getGrid(home.gridID);
  expect(tileCount(homeAfter), 'home grid gained no tile from the visit').toBe(tileCount(homeBefore));

  // Ascend: back on the home grid with nothing placed, and the ephemeral tile
  // deleted from the scratch grid. The grey border meant it would go.
  await gw.middleClickCell(0, 0);
  const back = await gw.focused();
  expect(back.gridID, 'still on the home grid after ascent').toBe(home.gridID);
  const homeFinal = await gw.getGrid(home.gridID);
  expect(tileCount(homeFinal), 'ascent leaves the home grid unchanged').toBe(tileCount(homeBefore));
  await expect
    .poll(async () => {
      const sc = await gw.getGrid(scratchGridID);
      return (sc.tiles ?? []).filter((t) => String(t.urlString ?? '').includes('example.com')).length;
    }, { timeout: 10_000 })
    .toBe(0);

  // And nothing surfaced on the error strip: neither the delete nor a stray
  // freeze.
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the ephemeral round trip').toHaveLength(0);
});
