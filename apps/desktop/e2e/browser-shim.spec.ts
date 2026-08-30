import { test, expect } from './fixtures';

// Issue #90: a terminal app that "opens a browser" (emacs browse-url,
// xdg-open — both consult $BROWSER) must open an ephemeral url visit
// instead of spawning Chrome on the host. Every new shell session carries
// BROWSER=<gridwell-open shim>; the shim emits the url as OSC 5522 through
// tmux's DCS passthrough; the terminal's OSC handler routes it into the
// live-tile link path — since #207 that opens the visit in a SPLIT BELOW,
// the shell staying live above. This spec crosses the WHOLE chain by
// running the real $BROWSER inside the real PTY — env injection → sh shim →
// tmux passthrough → the /shell WebSocket → xterm OSC → open below.

// shimCleanup ascends the ephemeral lower pane (deleting the visit),
// refocuses the shell pane, exits the shell, and deletes its tile so tmux
// dies pre-teardown.
async function shimCleanup(
  gw: any,
  window: any,
  shellPaneID: string,
  cx: number,
  cy: number,
): Promise<void> {
  const m = window.mouse;
  await expect
    .poll(
      async () => {
        const lp = (await gw.panes()).find((p: any) => p.focused);
        if (!lp) return 'no-pane';
        if (lp.id === shellPaneID) return '';
        if (lp.textFocus !== '') {
          await m.move(lp.x + lp.w / 2, lp.y + lp.h / 2);
          await m.down({ button: 'middle' });
          await m.up({ button: 'middle' });
        }
        return lp.textFocus;
      },
      { timeout: 15_000, intervals: [700, 700, 1000, 1500] },
    )
    .toBe('');
  const up = (await gw.panes()).find((p: any) => p.id === shellPaneID)!;
  await window.mouse.click(up.x + up.w / 2, up.y + up.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(shellPaneID);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
}

test('running $BROWSER in a shell opens an ephemeral url visit below', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // a drop lands bare (#241); the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPane = await gw.focused();
  const shellFocus = shellPane.textFocus;
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );

  // What a terminal app does: exec "$BROWSER" <url>. The local origin loads
  // instantly with no network.
  await window.keyboard.type(`"$BROWSER" ${gw.origin}/?opened=via-shim`);
  await window.keyboard.press('Enter');

  // The url comes back through the PTY as OSC 5522 and opens a live
  // ephemeral visit in a split BELOW; the shell pane never leaves its shell.
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  const inURL = await gw.focused();
  expect(inURL.id, 'a new pane took focus').not.toBe(shellPane.id);
  expect(inURL.textFocus, 'the new pane descended into a tile').toBeTruthy();
  const upper = (await gw.panes()).find((p) => p.id === shellPane.id)!;
  expect(upper.textFocus, 'the shell pane still shows the shell').toBe(shellFocus);

  await shimCleanup(gw, window, shellPane.id, cx, cy);
});

// Issue #166: $BROWSER is not enough — emacs browse-url execs xdg-open
// directly, and under a desktop session (XDG_CURRENT_DESKTOP set) the real
// xdg-open resolves via the DE mime handler, never reading $BROWSER, so the
// url leaks to the host browser. Every new shell session gets a shadow-bin
// dir prepended to PATH whose xdg-open (and friends) routes web urls into
// the same OSC 5522 descent. Simulating the DE makes this spec fail without
// the shadow even on a DE-less CI box (where bare xdg-open would fall back
// to $BROWSER and mask the gap).
test('xdg-open under a desktop session opens an ephemeral url visit below', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // a drop lands bare (#241); the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPane = await gw.focused();
  const shellFocus = shellPane.textFocus;
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );

  // What emacs browse-url does: exec xdg-open <url>, with the DE visible in
  // the environment.
  await window.keyboard.type(
    `XDG_CURRENT_DESKTOP=GNOME xdg-open ${gw.origin}/?opened=via-shadow`,
  );
  await window.keyboard.press('Enter');

  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  const inURL = await gw.focused();
  expect(inURL.id, 'a new pane took focus').not.toBe(shellPane.id);
  expect(inURL.textFocus, 'the new pane descended into a tile').toBeTruthy();
  const upper = (await gw.panes()).find((p) => p.id === shellPane.id)!;
  expect(upper.textFocus, 'the shell pane still shows the shell').toBe(shellFocus);

  await shimCleanup(gw, window, shellPane.id, cx, cy);
});
