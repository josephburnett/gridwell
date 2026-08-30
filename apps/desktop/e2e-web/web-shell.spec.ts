import { test, expect } from './fixtures';

// A live shell in a plain browser. The PTY rides a WebSocket on the web door,
// cookie-gated like every other page request, so a browser — a phone — has the
// same shell the desktop does.
//
// This spec is the whole chain with no Electron anywhere: the page's own
// WebSocket, the /shell door, OpenShell, the home namespace, a real tmux PTY,
// and xterm, with a byte typed here coming back as terminal output. The unit
// tests on either side, client/shellstream, client/shellwire, and
// internal/server, cannot see this composition.

const shellText = (window: any): Promise<string> =>
  window.evaluate(() => (window as any).__gridwellTest.shellText());

test('a browser attaches a live shell and its output comes back', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 20_000 }).not.toBe('');

  await window.keyboard.type('echo browser-shell-lives');
  await window.keyboard.press('Enter');
  await expect
    .poll(() => shellText(window), { timeout: 20_000 })
    .toContain('browser-shell-lives');

  // Nothing degraded quietly on the way: no capability notice about shells
  // needing the desktop app.
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices, 'no capability notice: the browser really attached').toEqual([]);

  // Leave clean: ascend and delete the tile so its tmux session dies before
  // teardown.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
