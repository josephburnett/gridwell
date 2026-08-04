import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #61: rename while descended — "name the room you're in". The bottom
// bar's CURRENT crumb shows the name (issue #213 — part of the bar, never
// floating over pane content); clicking its text opens an input; Enter
// commits a USER-owned name via the versioned rename. The server latches
// ownership (alt_user), so the automatic captures — a shell's foreground
// command baked in on detach, a url's page title on freeze — never overwrite
// a name the user chose.

test('the bar crumb names the grid you are in', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(String(well.altText ?? '')).toBe(''); // unnamed at creation
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // The title shows "unnamed"; RIGHT-click it and type the room's name.
  await expect.poll(async () => (await gw.barName()).label).toBe('unnamed');
  // REAL mouse click — a synthetic dispatchEvent has no default actions and
  // masked the blur-to-body bug that made real renames "do nothing" (#130).
  await gw.clickBarName('right');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await input.fill('kitchen');
  await input.press('Enter');

  // The name landed server-side on the CONTAINING well, and the crumb agrees.
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)?.altText ?? ''))
    .toBe('kitchen');
  await expect.poll(async () => (await gw.barName()).label).toBe('kitchen');
});

test('a user-set shell name survives the detach command capture', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // A placed shell (drag) auto-descends and spawns the PTY.
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // a drop lands bare (#241); the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // Rename it while inside — the tmux-pane-rename case.
  await gw.clickBarName('right'); // real mouse (see above)
  const input = window.locator('#gw-rename-input');
  await input.fill('my-work');
  await input.press('Enter');
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'shell', cx, cy)?.altText ?? ''))
    .toBe('my-work');

  // Ascend: the detach path captures the foreground command and calls
  // SetTileAlt as a NON-user write — it must defer to the user's name.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await window.waitForTimeout(1000); // the capture is async fire-and-forget
  expect(
    String(tileAt(await gw.getGrid(home.gridID), 'shell', cx, cy)?.altText ?? ''),
    'the capture must not overwrite the user name',
  ).toBe('my-work');

  // Leave clean: delete the shell so its tmux session dies before teardown.
  await gw.deleteTileCell(cx, cy);
});
