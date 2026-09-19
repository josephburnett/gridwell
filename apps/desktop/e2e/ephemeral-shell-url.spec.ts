import { test, expect } from './fixtures';

// A url activated in a live shell behaves like a link a live url view pops: the
// pane splits and the url opens as an ephemeral visit, off-grid in the scratch
// grid, in the lower half. The shell stays live above, and ascending the lower
// pane deletes the visit. Descending in place would stack the shell on the
// session-only ascent stash, which any place-restore drops.
//
// The terminal-cell link click cannot be hit-tested from the canvas, so this
// spec fires the callback xterm's link provider runs (shellVisitURL). Url
// detection is unit-tested in urlnorm.FindURLs.
test('clicking a url in a shell opens an ephemeral visit in a split below (#207)', async ({
  window,
  gw,
}) => {
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  await gw.enterPlugin('home');
  const scratchGridID = await gw.scratchGridID('home');
  const home = await gw.focused();

  const shellCx = Math.round(home.cx);
  const shellCy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', shellCx, shellCy);
  await gw.descendCell(shellCx, shellCy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPane = await gw.focused();
  const shellFocus = shellPane.textFocus;
  const homeWithShell = tileCount(await gw.getGrid(home.gridID)); // shell is a real placed tile
  const panesBefore = (await gw.panes()).length;

  // The visit targets the local sidecar origin, so the view loads with no
  // network and tears down cleanly.
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

  const upper = (await gw.panes()).find((p) => p.id === shellPane.id)!;
  expect(upper.textFocus, 'the shell pane is still descended in the shell').toBe(shellFocus);

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

  // Ascending the lower pane deletes its ephemeral visit. A click during the
  // descent transition is deliberately swallowed, so the middle-click retries.
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

  // Delete the shell tile at the end so its tmux session is killed and teardown
  // does not hang on a live PTY.
  const up = (await gw.panes()).find((p) => p.id === shellPane.id)!;
  await window.mouse.click(up.x + up.w / 2, up.y + up.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(shellPane.id);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(shellCx, shellCy);
});
