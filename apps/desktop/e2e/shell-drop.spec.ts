import { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { GridwellDriver } from './driver';
import { getTile, setFrozen, shellTmuxAlive, tileAt } from './oracle';

// A live shell whose stream drops under it, as a laptop's sleep drops it,
// with the node still up and the tmux running. Neither Electron nor the
// devtools network emulation ends a socket already open, so the page's own
// WebSocket and fetch are wrapped (the client reads both off window at each
// dial) and the cut ends what they opened: the /shell socket and the event
// stream alike, which is what a sleep does to both.

const wrapNetwork = () => {
  const w = window as any;
  const cut = { sockets: [] as WebSocket[], streams: [] as AbortController[], down: false };
  w.__gwCut = cut;
  w.WebSocket = new Proxy(w.WebSocket, {
    construct(target, args) {
      const s = new target(...args);
      cut.sockets.push(s);
      return s;
    },
  });
  const fetch0 = w.fetch.bind(w);
  w.fetch = (input: any, init: any = {}) => {
    const url = String(input?.url ?? input);
    if (!url.includes('/Subscribe')) return fetch0(input, init);
    if (cut.down) return Promise.reject(new TypeError('network down'));
    const ac = new AbortController();
    init.signal?.addEventListener('abort', () => ac.abort());
    cut.streams.push(ac);
    return fetch0(input, { ...init, signal: ac.signal });
  };
};

// The page reloads once with the wrap in place, so the event stream it opens
// at boot is one the cut can end.
async function wrapped(window: Page, gw: GridwellDriver): Promise<void> {
  await window.addInitScript(wrapNetwork);
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.sources();
  await gw.waitIdle();
}

// down keeps the event stream from reopening until up is called.
const cut = (window: Page, down = false) =>
  window.evaluate((d) => {
    const c = (window as any).__gwCut;
    c.down = d;
    for (const s of c.sockets.splice(0)) s.close(4000, 'cut');
    for (const a of c.streams.splice(0)) a.abort();
  }, down);

const shellLive = (window: Page) =>
  window.evaluate(() => (window as any).__gridwellTest.shellRenderer() as string);

const shellText = (window: Page) =>
  window.evaluate(() => (window as any).__gridwellTest.shellText() as string);

// The event stream is down while the network is: its retry raises the events
// notice, and its reopening resolves it.
const eventsDown = async (window: Page) =>
  ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).some(
    (n: any) => n.source === 'events',
  );

async function liveShell(gw: GridwellDriver) {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  const id = tileAt(await gw.getGrid(f.gridID), 'shell', cx, cy)!.id;
  return { cx, cy, id };
}

async function deleteShell(gw: GridwellDriver, cx: number, cy: number) {
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
}

test('a live shell whose stream drops shows its last picture', async ({ gw, window }) => {
  await wrapped(window, gw);
  const { cx, cy } = await liveShell(gw);

  // The pane draws the preview cache once the terminal goes, so the drop
  // must leave the terminal's own last frame there, as a park does: with one
  // pane on the shell nothing else mirrors it.
  const before = await gw.shellMirrors();
  await cut(window);
  await expect
    .poll(() => gw.shellMirrors(), { message: "the drop took the terminal's last frame", timeout: 15_000 })
    .toBeGreaterThan(before);
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.shellStandin()),
    'the pane has a picture to stand in',
  ).toBeTruthy();

  await deleteShell(gw, cx, cy);
});

test('a live shell whose stream drops rejoins the same tmux when the node is back', async ({ gw, window }) => {
  await wrapped(window, gw);
  const { cx, cy } = await liveShell(gw);
  await window.keyboard.type('printf "%s\\n" gw-before-drop');
  await window.keyboard.press('Enter');
  await gw.waitShellLine('gw-before-drop');

  // Down long enough that the pane is seen down, then back: the event
  // stream's reopening is the one signal the pane rejoins on.
  await cut(window, true);
  await expect.poll(() => shellLive(window), { timeout: 10_000 }).toBe('');
  await expect.poll(() => eventsDown(window), { timeout: 10_000 }).toBe(true);
  await window.evaluate(() => ((window as any).__gwCut.down = false));
  await expect
    .poll(() => shellLive(window), { message: 'the shell rejoined with no gesture', timeout: 20_000 })
    .not.toBe('');
  // The same tmux: what it printed before the drop is still on its screen.
  await expect.poll(() => shellText(window), { timeout: 10_000 }).toContain('gw-before-drop');
  await gw.shellAttached();

  await deleteShell(gw, cx, cy);
});

test('a dropped shell frozen meanwhile stays frozen when the node is back', async ({ gw, window }) => {
  await wrapped(window, gw);
  const { cx, cy, id } = await liveShell(gw);

  // Another screen freezes the shell while this one is away: the freeze is
  // the user's standing word, which the rejoin reads on the row as a descent
  // does.
  await cut(window, true);
  await expect.poll(() => shellLive(window), { timeout: 10_000 }).toBe('');
  await setFrozen(gw.origin, id, true);
  await window.evaluate(() => ((window as any).__gwCut.down = false));
  await expect.poll(() => eventsDown(window), { timeout: 20_000 }).toBe(false);
  await window.waitForTimeout(3_000);
  expect(await shellLive(window), 'a frozen shell does not rejoin').toBe('');
  expect((await getTile(gw.origin, id)).urlFrozen, 'the row is still frozen').toBe(true);
  expect(await shellTmuxAlive(gw.origin, id), 'the freeze kept the tmux').toBe(true);

  await deleteShell(gw, cx, cy);
});

test('a shell whose tmux went while the node was away stays down', async ({ gw, window }) => {
  await wrapped(window, gw);
  const { cx, cy, id } = await liveShell(gw);

  // The tmux ends itself after the cut, while the event stream is held down.
  await window.keyboard.type('(sleep 2; tmux kill-session) >/dev/null 2>&1 & disown');
  await window.keyboard.press('Enter');
  await cut(window, true);
  await expect.poll(() => shellLive(window), { timeout: 10_000 }).toBe('');
  await window.waitForTimeout(4_000);
  await window.evaluate(() => ((window as any).__gwCut.down = false));
  await expect.poll(() => eventsDown(window), { timeout: 20_000 }).toBe(false);
  await window.waitForTimeout(3_000);
  expect(await shellLive(window), 'a dead tmux is never recreated behind its preview').toBe('');
  expect(await shellTmuxAlive(gw.origin, id), 'the tmux is gone').toBe(false);

  await deleteShell(gw, cx, cy);
});
