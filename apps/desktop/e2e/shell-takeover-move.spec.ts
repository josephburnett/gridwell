import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaDoor } from './trace';

// One live surface per content tile, and taking it over moves it, for a shell
// as for a url: the terminal and its /shell socket go to the pane that takes
// it, so nothing reattaches and xterm keeps what it held. Entering a pane tile
// whose layout shows the shell moves it in, leaving hands it back, entering
// again moves it in again, and opening the tile in a split pane moves it
// there. Through all of it the node's shell door opens no second attachment.

test('a shell taken over moves with its terminal and socket, never reattaching', async ({
  gw,
  home,
  window,
}) => {
  const origin = new URL(window.url()).origin;
  const token = await loginToken(origin, homePassword(home));
  const shellViews = (): Promise<Record<string, string>> =>
    window.evaluate(() => (window as any).__gridwellTest.shellViews());
  const depth = (): Promise<number> =>
    window.evaluate(() => (window as any).__gridwellTest.workspace().depth);
  // The xterm host is one DOM element per terminal, so a mark set on it
  // survives only a move; a rebuilt terminal is a new element.
  const hosts = (): Promise<string[]> =>
    window.evaluate(() =>
      Array.from(document.querySelectorAll('.gw-shell-host')).map((h) => (h as HTMLElement).dataset.gwMark ?? ''),
    );
  const attaches = async (): Promise<number> =>
    (await dumpViaDoor(origin, token)).filter(
      (l) => l.src === 'shelldoor' && l.kind === 'pty' && l.msg === 'open',
    ).length;
  const shellText = (): Promise<string> => window.evaluate(() => (window as any).__gridwellTest.shellText());

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Split first: splitting a descended pane would ascend it.
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(left);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  const tile = (await shellViews())[left.id];
  expect(tile, 'the left pane holds the live shell').toBeTruthy();

  const marker = `gw-moved-${Math.random().toString(36).slice(2, 8)}`;
  await window.keyboard.type(`printf '%s\\n' ${marker}`);
  await window.keyboard.press('Enter');
  const markerRow = async () => (await shellText()).split('\n').some((l) => l.trim() === marker);
  await expect.poll(markerRow, { timeout: 10_000 }).toBe(true);
  await window.evaluate(() => {
    (document.querySelector('.gw-shell-host') as HTMLElement).dataset.gwMark = 'kept';
  });
  await expect.poll(attaches, { timeout: 10_000 }).toBe(1);

  // The one terminal, still the first one, holding the marker, on the one
  // socket, and in the pane named.
  const kept = async (holder: string, why: string) => {
    await expect.poll(async () => (await shellViews())[holder], { message: why, timeout: 15_000 }).toBe(tile);
    expect(Object.keys(await shellViews()), `${why}: one holder`).toEqual([holder]);
    expect(await hosts(), `${why}: the terminal was rebuilt`).toEqual(['kept']);
    await gw.focusPane((await gw.panes()).find((p: any) => p.id === holder)!);
    expect(await markerRow(), `${why}: the terminal lost its rows`).toBe(true);
    expect(await attaches(), `${why}: the shell reattached`).toBe(1);
  };

  // A pane tile from the right pane captures the layout with the shell pane in
  // it, so the captured copy of that pane takes the shell over.
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === right.id)!);
  const rf = await gw.focused();
  const px = Math.round(rf.cx) + 2; // clear of the shell tile at (cx, cy)
  const py = Math.round(rf.cy);
  await gw.openPalette();
  await gw.dragCreate('pane', px, py);
  expect(tileAt(await gw.getGrid(grid), 'pane', px, py)).toBeTruthy();
  await gw.descendCell(px, py);
  await expect.poll(depth).toBe(1);
  let inner = '';
  await expect
    .poll(async () => (inner = Object.keys(await shellViews()).find((p) => p !== left.id) ?? ''), {
      message: 'the inner pane took the shell',
      timeout: 15_000,
    })
    .not.toBe('');
  await kept(inner, 'entering the level');

  await gw.leaveWorkspace();
  await expect.poll(depth).toBe(0);
  await kept(left.id, 'leaving the level');

  // Entering the same pane tile again moves it in again.
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === right.id)!);
  await gw.descendCell(px, py);
  await expect.poll(depth).toBe(1);
  await expect
    .poll(async () => (inner = Object.keys(await shellViews()).find((p) => p !== left.id) ?? ''), {
      message: 'the inner pane took the shell again',
      timeout: 15_000,
    })
    .not.toBe('');
  await kept(inner, 're-entering the level');
  await gw.leaveWorkspace();
  await expect.poll(depth).toBe(0);
  await kept(left.id, 'leaving the level again');

  // The same shell opened in the split pane moves there.
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === right.id)!);
  await gw.descendCell(cx, cy);
  await kept(right.id, 'opening it in the split pane');

  // Delete the shell tile so tmux never hangs the harness close.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === left.id)!);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');
  await gw.deleteTileCell(cx, cy);
});
