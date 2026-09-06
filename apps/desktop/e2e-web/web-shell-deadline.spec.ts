import { test, expect } from './fixtures';

// The web door's deadline rule, for its other long-lived thing: the /shell
// WebSocket after hijack. A browser's PTY rides a WebSocket on the web door
// (web-shell.spec.ts proves the plain chain); this proves that connection
// OUTLIVES the door's declared header timeout.
//
// THE RULE: a declared timeout is a fact, and its test is a wait bound to its
// value; a timeout with no such test is untested. The web door declares
// ReadHeaderTimeout: 10s (server.WebDoorServer). net/http clears that deadline
// before the handler runs, and shell_door.go's websocket.Accept (coder/
// websocket) then hijacks the conn and owns its deadlines through context —
// detaching it from net/http's per-request timeout machinery entirely. So a
// live shell must survive a wall-clock wait longer than the declared timeout
// and still carry bytes both ways. No other spec idles a live shell past 10s,
// so this is the only guard that a future refactor keeping the shell on the
// door's request deadline — a streaming response instead of a hijack, say —
// would trip; the typed-after-the-wait command would never come back.
//
// HONEST SCOPE (see the PR): because coder/websocket takes the conn's
// deadlines off net/http, re-adding a ReadTimeout OR WriteTimeout to
// WebDoorServer does NOT cut this WebSocket (verified: the spec stays green
// with either added). Unlike the two DERIVED holds — the connection-door
// tunnel test and the in-package Connect-stream test, which fail the moment a
// door deadline returns — this one guards the survival property itself, not a
// live door deadline. It is the /shell half the issue asked for: the hijack
// shown outliving the declared header timeout.
//
// The wait is bound to the door's 10s ReadHeaderTimeout (a browser-mode spec
// cannot import the Go shape, so it is a wall-clock constant tied to that
// value by this comment, with a second of margin). ~11s of real time is the
// point: the wait IS the test and must not be shortened by lowering the door's
// timeout. It lives here in the browser-mode shell coverage `make check-web`
// runs, the only gate that exercises the real /shell WebSocket end to end.

const HEADER_TIMEOUT_MS = 10_000; // server.WebDoorServer ReadHeaderTimeout
const HOLD_MS = HEADER_TIMEOUT_MS + 1_000;

const shellText = (window: any): Promise<string> =>
  window.evaluate(() => (window as any).__gridwellTest.shellText());

test('a browser shell outlives the web door header timeout', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 20_000 }).not.toBe('');

  // Live before the wait: a byte typed here comes back as terminal output.
  await window.keyboard.type('echo shell-before-wait');
  await window.keyboard.press('Enter');
  await expect
    .poll(() => shellText(window), { timeout: 20_000 })
    .toContain('shell-before-wait');

  // Hold the hijacked WebSocket idle past the web door's declared header
  // timeout. A conn still on that deadline would be closed here.
  await window.waitForTimeout(HOLD_MS);

  // Live after the wait: the same WebSocket still carries bytes both ways.
  await window.keyboard.type('echo shell-after-wait');
  await window.keyboard.press('Enter');
  await expect
    .poll(() => shellText(window), { timeout: 20_000 })
    .toContain('shell-after-wait');

  // Nothing degraded quietly: no capability notice about shells needing the
  // desktop app.
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices, 'no capability notice: the browser really attached').toEqual([]);

  // Leave clean: ascend and delete the tile so its tmux session dies before
  // teardown.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
