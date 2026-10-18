import { test, expect } from './fixtures';
import { placeTile, tileAt } from '../e2e/oracle';

// The event stream has no cursor, so a change made while it is broken is told
// by nothing; the stream that re-opens owes a re-read of everything shown
// (retry.Events). It opens at the node's header, not at the next event, so
// the re-read runs even when nothing changes after the gap.
const gate = `(() => {
  const orig = window.fetch.bind(window);
  let held = null, release = null, live = null;
  window.__breakStream = () => {
    held = new Promise((r) => (release = r));
    if (live) live.error(new TypeError('network error'));
  };
  window.__mendStream = () => { const r = release; held = null; if (r) r(); };
  window.fetch = async (input, init) => {
    const url = typeof input === 'string' ? input : input.url;
    if (!String(url).endsWith('/gridwell.v1.Gridwell/Subscribe')) return orig(input, init);
    if (held) await held;
    const res = await orig(input, init);
    if (!res.body) return res;
    const reader = res.body.getReader();
    const body = new ReadableStream({
      start(ctl) { live = ctl; },
      async pull(ctl) {
        const { value, done } = await reader.read();
        if (done) ctl.close();
        else ctl.enqueue(value);
      },
      cancel() { reader.cancel(); },
    });
    return new Response(body, { status: res.status, statusText: res.statusText, headers: res.headers });
  };
})()`;

test('stream break: a change made in the gap reaches the view with no later event', async ({ gw, window, serve }) => {
  await window.addInitScript(gate);
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(t, 'markdown tile created').toBeTruthy();
  await gw.waitClientTileAt(f.id, cx, cy, t.id!);
  await gw.waitIdle();

  await window.evaluate(() => (window as any).__breakStream());
  await expect
    .poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices
      .some((n: any) => n.source === 'events'), { message: 'the client saw its stream break' })
    .toBe(true);
  // Another writer moves the tile while no stream is open; nothing after this
  // changes, so no event will ever say so.
  await placeTile(serve.origin, t.id!, f.gridID, cx + 2, cy, 1, 1);
  await window.evaluate(() => (window as any).__mendStream());

  await gw.waitClientTileAt(f.id, cx + 2, cy, t.id!);
  await expect
    .poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices
      .some((n: any) => n.source === 'events'), { message: 'the stream is back' })
    .toBe(false);
});
