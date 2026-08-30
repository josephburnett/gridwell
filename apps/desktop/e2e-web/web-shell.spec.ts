import { test, expect } from './fixtures';

// A LIVE SHELL in a plain browser — the point of the 2026-08-29 transport
// move (docs/simplify-plan.md S3). The PTY used to ride a second client
// stack that only the Electron app had (main → gRPC → the federation
// socket), so a browser — the phone — had no shell at all. It now rides a
// WebSocket on the web door, cookie-gated like every other page request.
//
// This spec is the whole chain with no Electron anywhere: the page's own
// WebSocket → the /shell door → OpenShell → the home namespace → a real
// tmux PTY → xterm, and a byte typed here comes back as terminal output.
// The unit tests on either side (client/shellstream, client/shellwire,
// internal/server) cannot see this composition; nothing else does.

const shellText = (window: any): Promise<string> =>
  window.evaluate(() => (window as any).__gridwellTest.shellText());

test('a browser attaches a live shell and its output comes back', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // a drop lands bare (#241); the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 20_000 }).not.toBe('');

  await window.keyboard.type('echo browser-shell-lives');
  await window.keyboard.press('Enter');
  await expect
    .poll(() => shellText(window), { timeout: 20_000 })
    .toContain('browser-shell-lives');

  // Nothing degraded quietly on the way: a browser used to get the
  // "live shells need the desktop app" capability notice here.
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices, 'no capability notice: the browser really attached').toEqual([]);

  // Leave clean: ascend and delete the tile so its tmux session dies before
  // teardown.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
