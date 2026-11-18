import * as http from 'node:http';
import { once } from 'node:events';
import type { AddressInfo } from 'node:net';
import { test, expect, homePassword, loginToken } from './fixtures';
import { dumpViaAPI } from './trace';

// The root window shows the client and nothing else, because main trusts the
// page there with window.gridwell. A drop of a link or a file on the canvas is
// the same renderer navigation a script's location.href is, so this drives
// the script.
test('the root window refuses a navigation off the client and keeps the client', async ({ electronApp, window, home }) => {
  const origin = new URL(window.url()).origin;
  const token = await loginToken(origin, homePassword(home));
  const away = http.createServer((_req, res) => res.end('<title>away</title>away'));
  away.listen(0, '127.0.0.1');
  await once(away, 'listening');
  const awayURL = `http://127.0.0.1:${(away.address() as AddressInfo).port}/`;
  try {
    await window.evaluate((u) => {
      location.href = u;
    }, awayURL);

    const rootURL = () => electronApp.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].webContents.getURL());
    const refused = async () =>
      (await dumpViaAPI(origin, token)).some((l) => l.src === 'window' && l.kind === 'nav-refused' && l.msg === awayURL);
    // Either the refusal is recorded or the window has left: the poll ends on
    // whichever happens, so a regression fails on the address, not a timeout.
    await expect
      .poll(async () => (await refused()) || !(await rootURL()).startsWith(origin + '/'), { timeout: 20_000 })
      .toBe(true);
    expect(await rootURL(), 'the root window left the client').toMatch(new RegExp('^' + origin + '/'));
    expect(await window.evaluate(() => !!(window as any).__gridwellTest), 'the client is still running').toBe(true);
  } finally {
    away.close();
  }
});
