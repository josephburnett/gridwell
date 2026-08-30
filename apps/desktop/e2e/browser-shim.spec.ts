import { test, expect } from './fixtures';

// A terminal app that opens a browser (emacs browse-url, xdg-open; both consult
// $BROWSER) must open an ephemeral url visit rather than spawn Chrome on the
// host. Every new shell session carries BROWSER set to the gridwell-open shim.
// The shim emits the url as OSC 5522 through tmux's DCS passthrough, and the
// terminal's OSC handler routes it into the live-tile link path, which opens
// the visit in a split below with the shell staying live above. This spec
// crosses the whole chain by running the real $BROWSER inside the real PTY: env
// injection, sh shim, tmux passthrough, the /shell WebSocket, xterm OSC, open
// below.

// shimCleanup ascends the ephemeral lower pane, deleting the visit, refocuses
// the shell pane, exits the shell, and deletes its tile so tmux dies before
// teardown.
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
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPane = await gw.focused();
  const shellFocus = shellPane.textFocus;
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );

  // What a terminal app does: exec "$BROWSER" <url>. The local origin loads
  // with no network.
  await window.keyboard.type(`"$BROWSER" ${gw.origin}/?opened=via-shim`);
  await window.keyboard.press('Enter');

  // The url comes back through the PTY as OSC 5522 and opens a live ephemeral
  // visit in a split below; the shell pane never leaves its shell.
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

// $BROWSER alone is not enough: emacs browse-url execs xdg-open directly, and
// under a desktop session (XDG_CURRENT_DESKTOP set) the real xdg-open resolves
// through the desktop's mime handler and never reads $BROWSER, leaking the url
// to the host browser. So every new shell session gets a shadow-bin dir
// prepended to PATH whose xdg-open, and its friends, route web urls into the
// same OSC 5522 descent. Simulating the desktop environment makes this spec
// fail without the shadow even on a CI box that has none, where bare xdg-open
// would fall back to $BROWSER and mask the gap.
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
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPane = await gw.focused();
  const shellFocus = shellPane.textFocus;
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );

  // What emacs browse-url does: exec xdg-open <url>, with the desktop
  // environment visible in the env.
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
