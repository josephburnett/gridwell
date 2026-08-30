import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Rename while descended: name the room you are in. The bottom bar's current
// crumb shows the name, as part of the bar rather than floating over pane
// content. Clicking its text opens an input, and Enter commits a user-owned name
// through the versioned rename. The server latches ownership in alt_user, so the
// automatic captures — a shell's foreground command on detach, a url's page
// title on freeze — never overwrite a name the user chose.

test('the bar crumb names the grid you are in', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(String(well.altText ?? '')).toBe(''); // unnamed at creation
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // The title shows "unnamed"; right-click it and type the room's name.
  await expect.poll(async () => (await gw.barName()).label).toBe('unnamed');
  // A real mouse click. A synthetic dispatchEvent runs no default actions, and
  // so cannot see a blur-to-body bug that makes real renames do nothing.
  await gw.clickBarName('right');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await input.fill('kitchen');
  await input.press('Enter');

  // The name landed server-side on the containing well, and the crumb agrees.
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)?.altText ?? ''))
    .toBe('kitchen');
  await expect.poll(async () => (await gw.barName()).label).toBe('kitchen');
});

test('clicking away commits a rename; untouched closes write nothing; Escape cancels', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // Blur commits: on a phone the keyboard's done key blurs, and typing a name
  // then tapping elsewhere must not discard it.
  await gw.clickBarName('right');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await input.fill('porch');
  await window.mouse.click(200, 200); // click away, with no Enter
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)?.altText ?? ''))
    .toBe('porch');
  const named = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;

  // An untouched input closed by clicking away writes nothing: reading never
  // mutates, and a no-op close must not bump the version.
  await gw.clickBarName('right');
  await expect(input).toBeVisible();
  await window.mouse.click(200, 200);
  await expect(input).toHaveCount(0);
  const after = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(after.version, 'no-op close must not write').toBe(named.version);

  // Escape still cancels an edit in progress.
  await gw.clickBarName('right');
  await expect(input).toBeVisible();
  await input.fill('discarded');
  await input.press('Escape');
  await expect(input).toHaveCount(0);
  expect(String(tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)?.altText ?? '')).toBe('porch');
});

test('an async tile event never steals the rename input focus', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  const inside = await gw.focused();
  const icx = Math.round(inside.cx);
  const icy = Math.round(inside.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', icx, icy);
  const doc = tileAt(await gw.getGrid(inside.gridID), 'text', icx, icy)!;

  // Open the rename, then land a foreign write on the text tile. The TileChanged
  // event refreshes the file overlay, and its focus-return arm must not call
  // canvas.focus() unconditionally: that yanks focus out of the input and
  // typing goes to the canvas. The guard must keep the input focused through it.
  await gw.clickBarName('right');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  const { updateText } = await import('./oracle');
  await updateText(gw.origin, doc.id, Number(doc.version ?? 0), '# poked from outside');
  await window.waitForTimeout(400); // let the event apply and the overlay refresh
  await expect
    .poll(() => window.evaluate(() => document.activeElement?.id ?? ''))
    .toBe('gw-rename-input');
  await window.keyboard.type('den');
  await window.keyboard.press('Enter');
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)?.altText ?? ''))
    .toBe('den');
});

test('a user-set shell name survives the detach command capture', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // A dragged shell tile lands bare; the descent spawns the PTY.
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // Rename it while inside: the tmux-pane-rename case.
  await gw.clickBarName('right'); // a real mouse click, as above
  const input = window.locator('#gw-rename-input');
  await input.fill('my-work');
  await input.press('Enter');
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'shell', cx, cy)?.altText ?? ''))
    .toBe('my-work');

  // Ascend: the detach path captures the foreground command and calls SetTileAlt
  // as a non-user write, so it must defer to the user's name.
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
