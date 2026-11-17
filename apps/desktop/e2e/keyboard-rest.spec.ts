import { test, expect } from './fixtures';
import { createText, getTile } from './oracle';

// The rest of the keyboard's class, one case each: nothing but the owner
// moves the keyboard, and an open input ends on Enter, Esc or a press
// outside it, committing as its blur does.

const inputOpen = (window: any) => window.locator('#gw-rename-input');

// wrapNetwork lets a spec cut the client's sockets and its event stream, as
// a sleep does; shell-drop.spec.ts's.
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

// E: a shell stream that opens while a rename is open, here a live shell
// rejoining when the node is back, leaves the input the keyboard and the
// half-typed name uncommitted.
test('a shell stream opening under an open rename leaves the input the typing', async ({ gw, window }) => {
  await window.addInitScript(wrapNetwork);
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.sources();
  await gw.waitIdle();
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  const before = { id: (await gw.focused()).textFocus };
  const live = () => window.evaluate(() => (window as any).__gridwellTest.shellRenderer() as string);

  await window.evaluate(() => {
    const c = (window as any).__gwCut;
    c.down = true;
    for (const s of c.sockets.splice(0)) s.close(4000, 'cut');
    for (const a of c.streams.splice(0)) a.abort();
  });
  await expect.poll(live, { timeout: 10_000 }).toBe('');
  await gw.clickBarName('right');
  await expect(inputOpen(window)).toBeVisible();
  await window.keyboard.type('ha');
  await window.evaluate(() => ((window as any).__gwCut.down = false));
  await expect.poll(live, { message: 'the shell rejoined', timeout: 20_000 }).not.toBe('');
  await window.waitForTimeout(300);
  await expect(inputOpen(window), 'the rename is still open').toBeVisible();
  expect(await window.evaluate(() => document.activeElement?.id), 'the input kept the keyboard').toBe('gw-rename-input');
  expect(await inputOpen(window).inputValue()).toBe('ha');
  // The detach names the tile after its command, which a typed name would
  // latch over.
  expect(String((await getTile(gw.origin, before.id)).altText ?? ''), 'nothing was committed').not.toBe('ha');
  await window.keyboard.press('Escape');
  await gw.ascendViaCrumb();
  await gw.deleteTileCell(cx, cy);
});

// H: a raw text a place restores at boot gives its editor the keyboard once
// it shows, with no press.
test('a raw text restored from its place takes the keyboard when its editor shows', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const t = await createText(gw.origin, f.gridID, Math.round(f.cx) + 1, Math.round(f.cy), 'restored');
  await window.goto(`${gw.origin}/${f.gridID}/${t.id.split('/').pop()}?e2e=1`);
  await window.waitForFunction(() => !!(window as any).__gridwellTest?.panes, null, { timeout: 30_000 });
  await expect.poll(() => gw.textareaValue(), { timeout: 15_000 }).toBe('restored');
  await expect
    .poll(() => window.evaluate(() => document.activeElement?.id), { message: 'the restored editor holds the keyboard' })
    .toBe('gw-text-editor');
});
