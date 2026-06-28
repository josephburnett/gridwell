import { test, expect } from './fixtures';

// Real-stack test for "descend into a url" via the menu: clicking (not dragging)
// the url swatch opens the url modal; submitting it descends into a LIVE url
// tile created in the plugin's off-grid scratch grid. The visit must NOT place
// a tile on the visible home grid, must go live (a native WebContentsView), and
// must leave nothing behind on ascent — the tile lives only in the scratch grid
// (visited-url history).
//
// Mostly server-observable (getGrid oracle), so it doesn't depend on the page
// actually loading; the one native check is that a live view got created.
test('clicking the menu url swatch descends into an off-grid ephemeral visit', async ({ electronApp, window, gw }) => {
  // An empty grid comes back with no tiles field (proto3 omits empty repeated),
  // so count defensively.
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  // The localdb's scratch grid id is advertised on the launcher tile.
  const local = (await (async () => {
    await window.waitForFunction(() => (window as any).__gridwellTest.launcher().length > 0);
    return gw.launcher();
  })()).find((l) => l.kind === 'localdb');
  expect(local, 'localdb plugin on the launcher').toBeTruthy();
  const scratchGridID = local!.scratchGridID;
  expect(scratchGridID, 'localdb advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const homeBefore = await gw.getGrid(home.gridID);
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);

  // Click (not drag) the url swatch → the ephemeral-visit modal opens.
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/ephemeral');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  // The visit goes live asynchronously (create → descend → open stream), so poll
  // until a native WebContentsView has been placed for it.
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // The ephemeral url landed in the OFF-GRID scratch grid, not on home.
  const scratch = await gw.getGrid(scratchGridID);
  const scratchURLs = (scratch.tiles ?? []).filter(
    (t) => t.kind === 'url' && String(t.urlString ?? '').includes('example.com'),
  );
  expect(scratchURLs, 'one ephemeral url tile in the scratch grid').toHaveLength(1);

  const homeAfter = await gw.getGrid(home.gridID);
  expect(tileCount(homeAfter), 'home grid gained no tile from the visit').toBe(tileCount(homeBefore));

  // Ascend: back on the home grid, still nothing placed — the visit is "gone".
  await gw.middleClickCell(0, 0);
  const back = await gw.focused();
  expect(back.gridID, 'still on the home grid after ascent').toBe(home.gridID);
  const homeFinal = await gw.getGrid(home.gridID);
  expect(tileCount(homeFinal), 'ascent leaves the home grid unchanged').toBe(tileCount(homeBefore));
});
