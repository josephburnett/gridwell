import { test, expect } from './fixtures';

// Issue #85: clicking (not dragging) the SHELL swatch opens an EPHEMERAL
// shell — created off-grid in the scratch grid, descended into, PTY spawned.
// Ascending DELETES it: the tile row is gone (the plugin kills the tmux
// session and all its processes as part of the delete), nothing lands on the
// home grid, and no error surfaces. Gray border while inside is the warning;
// the tile fact behind it (scratch-grid residence) is asserted via the oracle.

test('clicking the shell swatch opens an ephemeral shell; ascent deletes it', async ({
  window,
  gw,
}) => {
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  const local = (await gw.plugins()).find((l) => l.kind === 'localdb');
  const scratchGridID = local!.scratchGridID;
  expect(scratchGridID, 'localdb advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const homeBefore = await gw.getGrid(home.gridID);

  // Click (not drag) the shell swatch → descend straight into a live shell.
  await gw.clickPaletteSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // The shell tile lives in the OFF-GRID scratch grid, not on home.
  const scratch = await gw.getGrid(scratchGridID);
  const scratchShells = (scratch.tiles ?? []).filter((t) => t.kind === 'shell');
  expect(scratchShells, 'one ephemeral shell in the scratch grid').toHaveLength(1);
  expect(tileCount(await gw.getGrid(home.gridID)), 'home grid unchanged').toBe(
    tileCount(homeBefore),
  );

  // The bar's current crumb labels the dying context read-only (issue
  // #118's audit gap): "ephemeral" — and left-click must NOT open the
  // rename input.
  await expect.poll(async () => (await gw.barName()).label).toBe('ephemeral');
  await gw.clickBarName('right');
  await expect(window.locator('#gw-rename-input')).toHaveCount(0);

  // The terminal runs on the WEBGL renderer — never the canvas fallback,
  // whose dirty-region artifacts are the #84 bug class. Chromium silently
  // dropped software WebGL once (issue #128); this assertion makes any
  // future downgrade a loud suite failure.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()))
    .toBe('webgl');

  // It is a real terminal: type into it (keys go to the PTY via xterm).
  await window.keyboard.type('echo ephemeral-shell-proof');
  await window.keyboard.press('Enter');
  // Wait for echo's OUTPUT line — proof the keys crossed the PTY and came
  // back, with no wall-clock guess.
  await expect
    .poll(async () => {
      const t: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
      return t.split('\n').some((l) => l.includes('ephemeral-shell-proof') && !l.includes('echo '));
    }, { timeout: 10_000 })
    .toBe(true);

  // Ascend (bar crumb click): the tile is DELETED, tmux session included.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => (await gw.getGrid(scratchGridID)).tiles?.length ?? 0, { timeout: 10_000 })
    .toBe(0);
  expect(tileCount(await gw.getGrid(home.gridID)), 'ascent left home unchanged').toBe(
    tileCount(homeBefore),
  );

  // Nothing on the error strip: no stray freeze, no failed delete.
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the ephemeral shell round trip').toHaveLength(0);
});
