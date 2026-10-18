import { test, expect } from './fixtures';

// A live shell must outlive the web door's declared ReadHeaderTimeout of 10s
// (server.WebDoorServer), because shell_door.go's websocket.Accept hijacks the
// conn and owns its deadlines through context. No other spec idles a live shell
// past 10s, so a refactor that put it back on the door's request deadline is
// caught only here. The roughly 11s of real time is the test and must not be
// shortened; a browser-mode spec cannot import the Go constant, so this comment
// is the tie.

const HEADER_TIMEOUT_MS = 10_000; // server.WebDoorServer ReadHeaderTimeout
const HOLD_MS = HEADER_TIMEOUT_MS + 1_000;

const shellText = (window: any): Promise<string> =>
  window.evaluate(() => (window as any).__gridwellTest.shellText());

test('a browser shell outlives the web door header timeout', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 20_000 }).not.toBe('');

  await window.keyboard.type('echo shell-before-wait');
  await window.keyboard.press('Enter');
  await expect
    .poll(() => shellText(window), { timeout: 20_000 })
    .toContain('shell-before-wait');

  // A conn still on the door's deadline would be closed here.
  await window.waitForTimeout(HOLD_MS);

  await window.keyboard.type('echo shell-after-wait');
  await window.keyboard.press('Enter');
  await expect
    .poll(() => shellText(window), { timeout: 20_000 })
    .toContain('shell-after-wait');

  // A capability notice would mean the browser never attached.
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices, 'no capability notice: the browser really attached').toEqual([]);

  // Delete the tile so its tmux session dies before teardown.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
