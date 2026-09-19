import { test, expect } from './fixtures';

// Clicking the shell swatch, rather than dragging it, opens an ephemeral shell:
// created in the off-grid scratch grid, descended into, PTY spawned. Ascending
// deletes the tile and kills its tmux session. Nothing lands on the home grid
// and no error surfaces.

test('clicking the shell swatch opens an ephemeral shell; ascent deletes it', async ({
  window,
  gw,
}) => {
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  await gw.enterPlugin('home');
  const scratchGridID = await gw.scratchGridID('home');
  const home = await gw.focused();
  const homeBefore = await gw.getGrid(home.gridID);

  // Clicking the swatch descends into a live shell.
  await gw.clickPaletteSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // The tile lives in the scratch grid, and home is untouched.
  const scratch = await gw.getGrid(scratchGridID);
  const scratchShells = (scratch.tiles ?? []).filter((t) => t.kind === 'shell');
  expect(scratchShells, 'one ephemeral shell in the scratch grid').toHaveLength(1);
  expect(tileCount(await gw.getGrid(home.gridID)), 'home grid unchanged').toBe(
    tileCount(homeBefore),
  );

  // The crumb labels the dying context "ephemeral" and is read-only, so no
  // rename input opens.
  await expect.poll(async () => (await gw.barName()).label).toBe('ephemeral');
  await gw.clickBarName('right');
  await expect(window.locator('#gw-rename-input')).toHaveCount(0);

  // attachShellRenderer falls back to the slower DOM renderer when WebGL2 is
  // unavailable, and Chromium can drop software WebGL out from under it. This
  // assertion makes that downgrade a suite failure.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()))
    .toBe('webgl');

  // Typing sends the keys through xterm to the PTY.
  await window.keyboard.type('echo ephemeral-shell-proof');
  await window.keyboard.press('Enter');
  // Waiting for echo's output line proves the keys crossed the PTY and came
  // back, with no wall-clock guess.
  await expect
    .poll(async () => {
      const t: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
      return t.split('\n').some((l) => l.includes('ephemeral-shell-proof') && !l.includes('echo '));
    }, { timeout: 10_000 })
    .toBe(true);

  // Ascending by crumb click deletes the tile and its tmux session.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => (await gw.getGrid(scratchGridID)).tiles?.length ?? 0, { timeout: 10_000 })
    .toBe(0);
  expect(tileCount(await gw.getGrid(home.gridID)), 'ascent left home unchanged').toBe(
    tileCount(homeBefore),
  );

  // Nothing on the error strip.
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the ephemeral shell round trip').toHaveLength(0);
});
