import { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { GridwellDriver } from './driver';

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

async function liveShell(gw: GridwellDriver) {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  return { cx, cy };
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
