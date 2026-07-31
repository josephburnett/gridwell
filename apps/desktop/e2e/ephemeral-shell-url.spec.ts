import { test, expect } from './fixtures';

// Issue #207: a url activated in a live shell behaves exactly like a link a
// live url view pops (issue #111): the pane SPLITS and the url opens as an
// ephemeral visit (off-grid, in the scratch grid) in the lower half — the
// shell stays live and visible above, and ascending the lower pane deletes
// the visit (issue #85). The old behavior descended IN PLACE, stacking the
// shell on the session-only ascent stash — which any place-restore dropped
// (the issue #208 double-ascend).
//
// The terminal-cell link click itself can't be hit-tested from the canvas, so
// the e2e fires the exact callback xterm's link provider runs (shellVisitURL);
// url detection is unit-tested separately (urlnorm.FindURLs).
test('clicking a url in a shell opens an ephemeral visit in a split below (#207)', async ({
  electronApp,
  window,
  gw,
}) => {
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  const local = (await gw.plugins()).find((l) => l.kind === 'localdb');
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
  const shellPane = await gw.focused();
  const shellFocus = shellPane.textFocus;
  const homeWithShell = tileCount(await gw.getGrid(home.gridID)); // shell is a real placed tile
  const panesBefore = (await gw.panes()).length;

  // Activate a url in the shell → a new pane splits off BELOW and opens the
  // ephemeral visit; the shell pane is untouched. Visit the local sidecar
  // origin so the view loads instantly (no network) and tears down cleanly.
  const visitURL = `${gw.origin}/?visited=shx`;
  await gw.shellVisitURL(visitURL);
  await expect.poll(async () => (await gw.panes()).length, { timeout: 15_000 }).toBe(
    panesBefore + 1,
  );
  const panes = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  const lower = panes[panes.length - 1];
  expect(lower.focused, 'the new lower pane took focus').toBe(true);
  expect(lower.textFocus, 'the lower pane descended into a tile').toBeTruthy();
  expect(lower.textFocus, 'the lower pane shows the visit, not the shell').not.toBe(shellFocus);

  // The SHELL pane never left its shell — no stacked descent, nothing for a
  // place-restore to lose (issue #208).
  const upper = (await gw.panes()).find((p) => p.id === shellPane.id)!;
  expect(upper.textFocus, 'the shell pane is still descended in the shell').toBe(shellFocus);

  // The url landed in the off-grid scratch grid; the home grid still has only
  // the shell tile (the visit placed nothing on it).
  await expect
    .poll(async () => {
      const sc = await gw.getGrid(scratchGridID);
      return (sc.tiles ?? []).filter(
        (t) => t.kind === 'url' && String(t.urlString ?? '').includes('visited=shx'),
      ).length;
    }, { timeout: 10_000 })
    .toBe(1);
  expect(tileCount(await gw.getGrid(home.gridID)), 'home grid still just the shell').toBe(
    homeWithShell,
  );

  // Ascending the lower pane deletes its ephemeral visit. Wait out the
  // descent transition, then RETRYABLE middle-click at the lower pane's
  // center (a click mid-animation is deliberately swallowed).
  await gw.waitIdle();
  const m = window.mouse;
  await expect
    .poll(
      async () => {
        const lp = (await gw.panes()).find((p) => p.id === lower.id);
        if (!lp) return '';
        if (lp.textFocus !== '') {
          await m.move(lp.x + lp.w / 2, lp.y + lp.h / 2);
          await m.down({ button: 'middle' });
          await m.up({ button: 'middle' });
        }
        return lp.textFocus;
      },
      { timeout: 15_000, intervals: [700, 700, 1000, 1500] },
    )
    .toBe('');
  await gw.waitIdle();
  await expect
    .poll(async () => {
      const sc = await gw.getGrid(scratchGridID);
      return (sc.tiles ?? []).filter((t) => String(t.urlString ?? '').includes('visited=shx'))
        .length;
    }, { timeout: 10_000 })
    .toBe(0);

  // Leave clean: focus the shell pane (click its overlay), ascend out of the
  // shell (bar crumb click), then
  // delete the shell tile so its tmux session is killed and teardown doesn't
  // hang on a live PTY.
  const up = (await gw.panes()).find((p) => p.id === shellPane.id)!;
  await window.mouse.click(up.x + up.w / 2, up.y + up.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(shellPane.id);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(shellCx, shellCy);
});
