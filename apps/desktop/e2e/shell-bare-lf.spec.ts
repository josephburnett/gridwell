import { test, expect } from './fixtures';

// A bare LF must keep the cursor column, because tmux paints TUI output using
// LF as a keep-the-column index and xterm's convertEol would scatter characters
// down the left margin. The PTY path cannot reproduce it, since the inner PTY's
// ONLCR rewrites LFs to CRLF, so this feeds the terminal through shellFeed, the
// write path the /shell frames take.

const shellText = (window: any): Promise<string> =>
  window.evaluate(() => (window as any).__gridwellTest.shellText());

test('a bare LF keeps the cursor column (#211)', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const sx = Math.round(home.cx);
  const sy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', sx, sy);
  await gw.descendCell(sx, sy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  // tmux's attach paint erases anything written before it; one owner decides
  // when a write can land (driver.shellAttached).
  await gw.shellAttached();

  // \r\n starts at column 0 whatever the prompt; the marker after the bare \n
  // must land at column 13.
  const fed = await window.evaluate(() =>
    (window as any).__gridwellTest.shellFeed('\r\nCOLTEST-12345\nEND-MARKER\r\n'),
  );
  expect(fed, 'shellFeed found no live terminal').toBe(true);
  await expect
    .poll(
      async () => {
        const lines = ((await shellText(window)) as string).split('\n');
        return lines.find((l) => l.includes('END-MARKER')) ?? 'marker not visible';
      },
      { timeout: 15_000 },
    )
    .toMatch(/^ {13}END-MARKER/);

  // Delete the tile so its tmux session dies before teardown.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(sx, sy);
});
