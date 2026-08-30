import { test, expect } from './fixtures';

// Clicking the shell swatch, rather than dragging it, opens an ephemeral shell:
// created off-grid in the scratch grid, descended into, PTY spawned. Ascending
// deletes it. The tile row is gone, the delete kills the tmux session and all
// its processes, nothing lands on the home grid, and no error surfaces. The
// grey border while inside is the warning; the oracle asserts the fact behind
// it, that the tile lives in the scratch grid.

test('clicking the shell swatch opens an ephemeral shell; ascent deletes it', async ({
  window,
  gw,
}) => {
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  const local = (await gw.plugins()).find((l) => l.kind === 'home');
  const scratchGridID = local!.scratchGridID;
  expect(scratchGridID, 'localdb advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('home');
  const home = await gw.focused();
  const homeBefore = await gw.getGrid(home.gridID);

  // Click the shell swatch, rather than dragging: descend into a live shell.
  await gw.clickPaletteSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // The shell tile lives in the off-grid scratch grid, not on home.
  const scratch = await gw.getGrid(scratchGridID);
  const scratchShells = (scratch.tiles ?? []).filter((t) => t.kind === 'shell');
  expect(scratchShells, 'one ephemeral shell in the scratch grid').toHaveLength(1);
  expect(tileCount(await gw.getGrid(home.gridID)), 'home grid unchanged').toBe(
    tileCount(homeBefore),
  );

  // The bar's current crumb labels the dying context read-only, as "ephemeral",
  // and a left-click must not open the rename input.
  await expect.poll(async () => (await gw.barName()).label).toBe('ephemeral');
  await gw.clickBarName('right');
  await expect(window.locator('#gw-rename-input')).toHaveCount(0);

  // The terminal runs on the WebGL renderer, never the canvas fallback, whose
  // dirty-region artifacts show as corrupted text. Chromium can drop software
  // WebGL out from under it, so this assertion makes such a downgrade a loud
  // suite failure.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()))
    .toBe('webgl');

  // It is a real terminal: typing sends the keys through xterm to the PTY.
  await window.keyboard.type('echo ephemeral-shell-proof');
  await window.keyboard.press('Enter');
  // Wait for echo's output line, which proves the keys crossed the PTY and came
  // back, with no wall-clock guess.
  await expect
    .poll(async () => {
      const t: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
      return t.split('\n').some((l) => l.includes('ephemeral-shell-proof') && !l.includes('echo '));
    }, { timeout: 10_000 })
    .toBe(true);

  // Ascend by clicking the bar crumb: the tile is deleted, tmux session
  // included.
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
