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
  await gw.enterPlugin('local');
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
  await gw.enterPlugin('local');
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
  const winH = await window.evaluate(() => window.innerHeight);
  const bottom = Math.max(...panes.map((p: any) => p.y + p.h));
  // Panes reclaim the full window height — no strip, and no reserved bar
  // band either: the bar rides INSIDE the focused pane (#220), a border's
  // width above its bottom edge (#223 — the border wraps around).
  expect(bottom, 'panes reclaim the reserved strip height').toBe(winH);
  const gap = bottom - (bar.top + bar.height);
  expect(gap, 'the band sits inside the pane border').toBeGreaterThan(0);
  expect(gap, 'by exactly the border width').toBeLessThan(8);
});

test('a rejected text save surfaces and reconciles instead of lingering as saved', async ({ gw, window }) => {
  await gw.enterPlugin('local');
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
  // A REAL Connect rejection body (OutcomeRejected): an aborted request is
  // the TRANSPORT contract — kept and retried, pinned by web-outage — and
  // would eventually land the suffix, inverting this spec's assertion.
  await window.route('**/gridwell.v1.Gridwell/WriteContent', (r: any) =>
    r.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({ code: 'invalid_argument', message: 'e2e: write refused' }),
    }),
  );
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
  await gw.enterPlugin('local');

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

// setupWellReframe is the shared body of the two framing-writeback failure
// specs (#156 + the 2026-08-14 transport split): create a well, descend,
// note the parent's cached signature, install `route` over SetTile, then
// reframe so the settle persister posts into it. Returns what the
// assertions need.
async function setupWellReframe(
  gw: any,
  window: any,
  route: (r: any) => void,
): Promise<{ parentGrid: string; wellID: string; sig0: Record<string, string> }> {
  await gw.enterPlugin('local');
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

  // Every framing writeback now fails, in the shape `route` decides.
  await window.route('**/gridwell.v1.Gridwell/SetTile', route);

  // Reframe inside the well — with a delivery ack: a synthetic wheel under
  // xvfb can be dropped, and a lost gesture leaves the settle persister
  // with NOTHING to persist (the pre-2026-08-07 inverse flake: isolated
  // runs saw zero SetTile posts and this spec timed out on the far-end
  // notice with no way to say which stage went quiet). The pane's own
  // framing is the ack; resending an undelivered wheel is harness
  // recovery, not the property under test.
  const z0 = (await gw.focused()).zoom;
  let reframed = false;
  for (let attempt = 0; attempt < 5 && !reframed; attempt++) {
    await gw.wheelAtFocusedCenter(-300);
    try {
      await expect.poll(async () => (await gw.focused()).zoom, { timeout: 2_000 }).not.toBe(z0);
      reframed = true;
    } catch {
      // wheel lost before the app — send again
    }
  }
  if (!reframed) throw new Error('the wheel reframe never landed after 5 sends');
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);

  // Staged attribution: the 600ms settle persister must flush and POST the
  // changed framing (persistPosts counters) — so the far-end notice below
  // can only fail for far-end reasons.
  await expect
    .poll(
      () => window.evaluate(() => (window as any).__gridwellTest.persistPosts().SetWellView ?? 0),
      { message: 'the settle persister posts SetWellView for the changed framing', timeout: 10_000 },
    )
    .toBeGreaterThan(0);
  await expect
    .poll(async () => {
      const e = await errors(window);
      return (e.notices ?? []).some((n: any) => n.source === 'rpc:SetWellView');
    }, { timeout: 10_000 })
    .toBe(true);

  return { parentGrid, wellID, sig0 };
}

test('a framing writeback REJECTED by the server rolls the optimistic patch back (issue #156)', async ({
  gw,
  window,
}) => {
  // persistWellView patches the cache BEFORE posting SetWellView (so the
  // parent preview updates instantly). If the SERVER REJECTS the write for
  // a non-conflict reason — it spoke, and said no — the patch must roll
  // back: otherwise a sibling pane's well preview shows framing the server
  // refused, silently snapping back on the next reload (charter §7). The
  // rejection is a real Connect error body (the wire shape the client
  // classifies as OutcomeRejected); a network ABORT is the OTHER contract,
  // pinned by the transport spec below.
  const { parentGrid, wellID, sig0 } = await setupWellReframe(gw, window, (r: any) =>
    r.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({ code: 'invalid_argument', message: 'e2e: framing write refused' }),
    }),
  );

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

test('a framing writeback lost to TRANSPORT keeps the patch (2026-08-14)', async ({
  gw,
  window,
}) => {
  // The other half of the contract: an ABORTED request means the server
  // NEVER SPOKE — the patched framing is the user's settled viewport and
  // the only copy of it, so rolling it back would lose the value (the old
  // behavior: any failure refetched, and against a flapping link the
  // refetch could succeed and silently revert the wheel). The patch stays
  // on screen and the write parks in the pending ledger; the reborn-server
  // drain is proven end-to-end by web-outage.spec.ts.
  const { parentGrid, wellID, sig0 } = await setupWellReframe(gw, window, (r: any) => r.abort());

  // The patch STAYS: the parent's cached signature keeps the reframed
  // view_* (checked steadily, not just once — a late rollback is the bug).
  for (let i = 0; i < 5; i++) {
    const sigs = await window.evaluate(
      (gid: string) => (window as any).__gridwellTest.gridSigs(gid),
      parentGrid,
    );
    expect(sigs[wellID], 'the transport-failed patch must stay').not.toBe(sig0[wellID]);
    await window.waitForTimeout(300);
  }
  await window.unroute('**/gridwell.v1.Gridwell/SetTile');
});
