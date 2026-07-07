import { test, expect } from './fixtures';

// Issue #90: a terminal app that "opens a browser" (emacs browse-url,
// xdg-open — both consult $BROWSER) must descend into an ephemeral url tile
// instead of spawning Chrome on the host. Every new shell session carries
// BROWSER=<gridwell-open shim>; the shim emits the url as OSC 5522 through
// tmux's DCS passthrough; the terminal's OSC handler routes it into the
// existing ephemeral-visit descent. This spec crosses the WHOLE chain by
// running the real $BROWSER inside the real PTY — env injection → sh shim →
// tmux passthrough → WebSocket → xterm OSC → descend.

test('running $BROWSER in a shell descends into an ephemeral url visit', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellFocus = (await gw.focused()).textFocus;
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );

  // What a terminal app does: exec "$BROWSER" <url>. The local origin loads
  // instantly with no network.
  await window.keyboard.type(`"$BROWSER" ${gw.origin}/?opened=via-shim`);
  await window.keyboard.press('Enter');

  // The url comes back through the PTY as OSC 5522 and descends into a live
  // ephemeral visit.
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  const inURL = await gw.focused();
  expect(inURL.textFocus, 'descended off the shell into the url').not.toBe(shellFocus);
  expect(inURL.textFocus, 'descended into a tile').toBeTruthy();

  // One ascent returns to the shell (it went inactive, not gone).
  await gw.middleClickCell(0, 0);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(shellFocus);

  // Leave clean: ascend out and delete the shell so tmux dies pre-teardown.
  await gw.rightClickPlus();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
