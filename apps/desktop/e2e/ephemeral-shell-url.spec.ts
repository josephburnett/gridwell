import { test, expect } from './fixtures';

// Real-stack test for "descend into a url clicked in a shell": from inside a
// live shell, activating a url descends into a live ephemeral visit (off-grid,
// in the scratch grid), and — unlike the menu case — ASCENDING returns to the
// shell (it went inactive, not gone), with nothing left on the grid.
//
// The terminal-cell link click itself can't be hit-tested from the canvas, so
// the e2e fires the exact callback xterm's link provider runs (shellVisitURL);
// url detection is unit-tested separately (urlnorm.FindURLs).
test('clicking a url in a shell descends, then ascends back to the shell', async ({ electronApp, window, gw }) => {
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  const local = (await (async () => {
    await window.waitForFunction(() => (window as any).__gridwellTest.launcher().length > 0);
    return gw.launcher();
  })()).find((l) => l.kind === 'localdb');
  const scratchGridID = local!.scratchGridID;
  expect(scratchGridID, 'localdb advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('localdb');
  const home = await gw.focused();

  // Drop a shell at an on-screen cell and descend into it (CreateShell
  // auto-descends + spawns the PTY). The create→descend is async, so poll until
  // the descent lands.
  const shellCx = Math.round(home.cx);
  const shellCy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', shellCx, shellCy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellFocus = (await gw.focused()).textFocus;
  const homeWithShell = tileCount(await gw.getGrid(home.gridID)); // shell is a real placed tile
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);

  // Activate a url in the shell → descend into a live ephemeral visit. Visit the
  // local sidecar origin so the view loads instantly (no network) and tears down
  // cleanly.
  const visitURL = `${gw.origin}/?visited=shx`;
  await gw.shellVisitURL(visitURL);
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const inURL = await gw.focused();
  expect(inURL.textFocus, 'now descended into the url, not the shell').not.toBe(shellFocus);
  expect(inURL.textFocus, 'descended into a tile').toBeTruthy();

  // The url landed in the off-grid scratch grid; the home grid still has only
  // the shell tile (the visit placed nothing on it).
  const scratch = await gw.getGrid(scratchGridID);
  const scratchURLs = (scratch.tiles ?? []).filter((t) => t.kind === 'url' && String(t.urlString ?? '').includes('visited=shx'));
  expect(scratchURLs, 'one ephemeral url tile in the scratch grid').toHaveLength(1);
  expect(tileCount(await gw.getGrid(home.gridID)), 'home grid still just the shell').toBe(homeWithShell);

  // Ascend ONCE: back in the shell (inactive → active again), not on the grid.
  await gw.middleClickCell(0, 0);
  expect((await gw.focused()).textFocus, 'one ascent returns to the shell').toBe(shellFocus);
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length))
    .toBe(wcBefore);

  // Leave clean: ascend out of the shell to the grid (the shell's corner circle,
  // not a middle-click — its overlay only forwards the right button), then delete
  // the shell tile so its tmux session is killed and electronApp teardown doesn't
  // hang on a live PTY.
  await gw.rightClickPlus();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(shellCx, shellCy);
});
