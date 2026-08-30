import { test, expect } from './fixtures';

// A bare LF must keep the cursor column. tmux paints TUI output, through
// scroll-region feeds and row emission, using LF as a keep-the-column index;
// xterm's convertEol option snaps every bare LF to column 0 and scatters
// characters down the left margin. The PTY path cannot reproduce this, because
// the inner PTY's ONLCR rewrites a shell command's LFs to CRLF, so the spec
// feeds the terminal directly through the shellFeed hook, the same write path
// the /shell WebSocket's frames take.

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

  // \r\n first, to start at column 0 regardless of the prompt. Then 13 chars, a
  // bare \n, and a marker: the marker must land at column 13. tmux knows nothing
  // about the injected text and may repaint over it at any moment, so each poll
  // attempt feeds and reads in one go and asserts on that snapshot; polling then
  // re-reading races the repaint and reads back nothing.
  await expect
    .poll(
      async () => {
        const fed = await window.evaluate(() =>
          (window as any).__gridwellTest.shellFeed('\r\nCOLTEST-12345\nEND-MARKER\r\n'),
        );
        if (!fed) return 'feed refused';
        const lines = ((await shellText(window)) as string).split('\n');
        return lines.find((l) => l.includes('END-MARKER')) ?? 'marker not visible';
      },
      { timeout: 15_000 },
    )
    .toMatch(/^ {13}END-MARKER/);

  // Leave clean: ascend and delete the shell tile so its tmux session dies and
  // teardown does not hang on a live PTY.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(sx, sy);
});
