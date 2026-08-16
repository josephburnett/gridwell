import { test, expect } from './fixtures';

// Real-stack test for "descend into a url" via the menu: clicking (not dragging)
// the url swatch opens the url modal; submitting it descends into a LIVE url
// tile created in the plugin's off-grid scratch grid. The visit must NOT place
// a tile on the visible home grid, must go live (a native WebContentsView), and
// must leave NOTHING behind on ascent — since issue #85, ephemeral means
// ephemeral: the ascent DELETES the scratch tile (no history record), and no
// error surfaces (the old always-freeze used to fail "descent path is invalid"
// on every ephemeral ascent).
//
// Mostly server-observable (getGrid oracle), so it doesn't depend on the page
// actually loading; the one native check is that a live view got created.
test('clicking the menu url swatch descends into an off-grid ephemeral visit', async ({ electronApp, window, gw }) => {
  // An empty grid comes back with no tiles field (proto3 omits empty repeated),
  // so count defensively.
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  // The localdb's scratch grid id is advertised on its plugin entry.
  const local = (await gw.plugins()).find((l) => l.kind === 'local');
  expect(local, 'localdb plugin configured').toBeTruthy();
  const scratchGridID = local!.scratchGridID;
  expect(scratchGridID, 'localdb advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('local');
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

  // Ascend: back on the home grid, nothing placed — and the ephemeral tile is
  // DELETED from the scratch grid (gray means gone, issue #85).
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
  // freeze (the pre-#85 always-freeze failed with "descent path is invalid").
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the ephemeral round trip').toHaveLength(0);
});
