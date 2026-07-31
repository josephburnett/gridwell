import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Crosses the failure-surfacing seam (issue #45): a real RPC failing at the
// real transport must produce a visible notice on the canvas strip — not a
// console line — and the strip must be reserved layout no pane (or native
// view) can cover. Failures are injected by aborting the Connect route with
// Playwright, so the whole path RPC-error → reactToErr → errsurface → strip
// runs against the live stack with no timing races.

async function errors(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.errors());
}

test('a failed mutation RPC surfaces a dismissible notice on the strip', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  // Sever PlaceTile at the transport and drag the tile one cell.
  await window.route('**/gridwell.v1.Gridwell/PlaceTile', (r: any) => r.abort());
  await gw.dragTileCell(cx, cy, cx + 1, cy);

  // The failure lands on the strip, as an error, attributed to the RPC.
  await expect
    .poll(async () => {
      const e = await errors(window);
      return e.notices.find((n: any) => n.source === 'rpc:PlaceTile')?.severity ?? null;
    }, { timeout: 10_000 })
    .toBe('error');

  // The strip is reserved layout: every pane ends at or above its top edge,
  // so no WebContentsView bound to a pane rect can ever cover a notice.
  const e = await errors(window);
  expect(e.stripH).toBeGreaterThan(0);
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  for (const p of panes) {
    expect(p.y + p.h, `pane ${p.id} must end above the notice strip`)
      .toBeLessThanOrEqual(e.stripTop + 0.5);
  }

  // Server state never moved (the optimistic ghost snapped back).
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx, cy), 'tile still at origin').toBeTruthy();

  await window.unroute('**/gridwell.v1.Gridwell/PlaceTile');

  // Clicking the row dismisses it and returns the layout to full height.
  await gw.clickScreen(200, e.stripTop + 5);
  await expect
    .poll(async () => (await errors(window)).stripH)
    .toBe(0);
});

// Crosses the expiry seam: a one-shot failure must leave the strip BY ITSELF
// once its source goes quiet (errsurface.ExpireAfter of silence), returning
// the reserved height to the panes with no user gesture — through the real
// timer, the real Expire prune, and the real relayout. The sticky exemption
// (plugin health, backend exit persist until resolved/dismissed) is pinned by
// the errsurface unit tests; this proves the live wiring actually fires.
test('a one-shot notice expires off the strip once its source goes quiet', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx, cy), 'markdown tile created').toBeTruthy();

  // One failed mutation, then the failure stops (unroute): the canonical
  // one-shot. E.g. equivalent to a URL that failed to load once.
  await window.route('**/gridwell.v1.Gridwell/PlaceTile', (r: any) => r.abort());
  await gw.dragTileCell(cx, cy, cx + 1, cy);
  await expect
    .poll(async () => (await errors(window)).notices.some((n: any) => n.source === 'rpc:PlaceTile'))
    .toBe(true);
  await window.unroute('**/gridwell.v1.Gridwell/PlaceTile');

  // No click, no dismiss: the strip must clear on its own and the panes get
  // their height back. ExpireAfter is 10s; poll well past it.
  await expect
    .poll(async () => (await errors(window)).stripH, { timeout: 20_000, intervals: [1_000] })
    .toBe(0);
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
  const bottom = Math.max(...panes.map((p: any) => p.y + p.h));
  // Panes end at the always-on bottom bar (issue #212) — the strip's band
  // below it is reclaimed.
  expect(bottom, 'panes reclaim the reserved strip height').toBe(bar.top);
});

test('a rejected text save surfaces and reconciles instead of lingering as saved', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  // Establish server truth: type, ascend (flushes), verify persisted.
  await gw.descendCell(cx, cy);
  await gw.typeText('saved-content');
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('saved-content');

  // Re-descend, sever WriteContent, and type. The debounced save must fail
  // VISIBLY: before issue #45 this was the literal "it just disappeared"
  // mechanism — the rejected bytes kept rendering as saved with no signal.
  await gw.descendCell(cx, cy);
  await window.route('**/gridwell.v1.Gridwell/WriteContent', (r: any) => r.abort());
  await gw.typeText(' rejected-suffix');
  await expect
    .poll(async () => {
      const e = await errors(window);
      return e.notices.some((n: any) => n.source === 'rpc:WriteContent');
    }, { timeout: 15_000 })
    .toBe(true);
  await window.unroute('**/gridwell.v1.Gridwell/WriteContent');

  // Reconcile: the server's body is untouched — and the client did not keep
  // claiming otherwise (the strip said so; the cache dropped the bytes).
  const body = await gw.getTileContent(created.id);
  expect(body).toContain('saved-content');
  expect(body).not.toContain('rejected-suffix');
});

// Crosses the Electron main-process error seam (issue #46): a live URL tile's
// did-fail-load was previously unhandled ANYWHERE — the native WebContentsView
// just sat blank with zero signal to the user. This descends into a real
// (unreachable) address, so the real Electron layer fires a real net error on
// the real WebContentsView, which must reach the wasm errsurface over gw:error
// attributed to 'electron:webview' — not an rpc: source, since no RPC is
// involved at all.
test('an unreachable live URL tile surfaces a did-fail-load notice from the Electron layer', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');

  // The ephemeral-visit modal (clicking, not dragging, the url palette swatch)
  // descends straight into a live url tile — see ephemeral-url.spec.ts. Port 9
  // ("discard") has nothing listening on a normal machine, so Chromium's
  // connection attempt is refused immediately (ERR_CONNECTION_REFUSED) rather
  // than hanging on a timeout.
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'http://127.0.0.1:9/');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  await expect
    .poll(async () => {
      const e = await errors(window);
      return e.notices.find((n: any) => n.source === 'electron:webview')?.severity ?? null;
    }, { timeout: 15_000 })
    .toBe('error');

  const e = await errors(window);
  const notice = e.notices.find((n: any) => n.source === 'electron:webview');
  expect(notice.message).toContain('127.0.0.1:9');
});

test('a rejected framing writeback rolls the optimistic patch back (issue #156)', async ({
  gw,
  window,
}) => {
  // persistWellView patches the cache BEFORE posting SetWellView (so the
  // parent preview updates instantly). If the server rejects the write for a
  // NON-conflict reason, the patch must roll back — otherwise a sibling
  // pane's well preview shows framing the server refused, silently snapping
  // back on the next reload (charter §7). The observable seam is the
  // MID-DESCENT settle persist: the ascent path refetches the parent anyway,
  // but the 600ms settle persister patches and posts while you stay
  // descended — so the sibling preview is where the rejected patch lives.
  await gw.enterPlugin('localdb');
  const parentGrid = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // The parent grid's cached signatures before any reframe. Read via the
  // gesture-free gridSigs hook: a focus click would REFETCH the grid it
  // lands on, healing exactly the divergence this spec observes.
  const sig0 = await window.evaluate(
    (gid: string) => (window as any).__gridwellTest.gridSigs(gid),
    parentGrid,
  );
  const wellID = Object.keys(sig0)[0];
  expect(wellID, 'the parent grid holds the well').toBeTruthy();

  // Every framing writeback now fails (non-conflict).
  await window.route('**/gridwell.v1.Gridwell/SetTile', (r: any) => r.abort());

  // Reframe inside the well; the 600ms settle persister patches the parent
  // cache, posts SetWellView, and is rejected.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  await expect
    .poll(async () => {
      const e = await errors(window);
      return (e.notices ?? []).some((n: any) => n.source === 'rpc:SetWellView');
    }, { timeout: 10_000 })
    .toBe(true);

  // The rejected patch must not stick: the parent cache reconciles back to
  // server truth (the original view_*). Pre-fix this never reconciles — the
  // patched framing sits in the cache until an unrelated gesture refetches.
  await expect
    .poll(
      async () => {
        const sigs = await window.evaluate(
          (gid: string) => (window as any).__gridwellTest.gridSigs(gid),
          parentGrid,
        );
        return sigs[wellID] === sig0[wellID];
      },
      { timeout: 10_000 },
    )
    .toBe(true);
  await window.unroute('**/gridwell.v1.Gridwell/SetTile');
});
