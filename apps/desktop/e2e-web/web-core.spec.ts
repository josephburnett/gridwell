import { test, expect, authHeaders } from './fixtures';
import { tileAt } from '../e2e/oracle';

// Crosses the browser-mode seam: the full core loop — boot, enter a plugin,
// create a tile, descend, type, persist — must work with NO Electron shell
// and no window.gridwell bridge, against the same server the desktop uses.
// The errors() hook at the end is the "nothing degraded silently" oracle.
test('the plain-browser client boots, creates, and edits', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created from a plain browser').toBeTruthy();

  await gw.descendCell(cx, cy);
  await gw.typeText('written from a plain browser');
  await gw.ascendViaCrumb(); // ascend flushes the text save
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('written from a plain browser');

  // No notice on the strip: browser mode must not be quietly erroring its
  // way through the core loop.
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices).toEqual([]);
});

// #259: shell CREATION needs an attach capability, not just node policy.
// A plain browser has no PTY bridge — a shell created there could never
// attach, dead weight from birth — so the swatch is gone even on a
// shells-ENABLED node. Viewing stays: a shell tile created elsewhere
// (here: through the server oracle, as the desktop app would) still
// lands in the client's world. url tiles deliberately remain creatable
// (recording an address is useful without a live view).
test('the browser offers no shell creation, but views shell tiles fine', async ({ gw, window, serve }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();

  await gw.openPalette();
  const pal = await gw.palette();
  const primitives = pal.items.filter((i: any) => !i.isPlugin).map((i: any) => i.kind);
  expect(primitives, 'url stays creatable in the browser').toContain('url');
  expect(primitives, 'no attach capability → no shell swatch').not.toContain('shell');
  await window.keyboard.press("Escape");
  await gw.waitIdle();

  // A desktop-made shell tile (oracle create) still renders here.
  const cx = Math.round(f.cx) + 2;
  const cy = Math.round(f.cy);
  const res = await fetch(`${gw.origin}/gridwell.v1.Gridwell/CreateTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(serve) },
    body: JSON.stringify({
      gridId: f.gridID,
      tile: { kind: 'shell', x: cx, y: cy, w: 1, h: 1, altText: 'desktop shell' },
    }),
  });
  expect(res.ok, 'the SERVER still accepts shell creates (policy is unchanged)').toBe(true);
  const made = (await res.json()).tile;
  await expect
    .poll(async () => {
      const sigs = await window.evaluate(
        (gid: string) => (window as any).__gridwellTest.gridSigs(gid),
        f.gridID,
      );
      return Object.keys(sigs).includes(made.id);
    })
    .toBe(true);
});
