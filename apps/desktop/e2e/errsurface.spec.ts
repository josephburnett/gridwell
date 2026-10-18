import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A real RPC failing at the real transport must produce a visible notice, and
// the strip must be reserved layout no pane or native view can cover. Failures
// are injected by aborting the Connect route, so the path from RPC error
// through reportErr and errsurface runs against the live stack.

async function errors(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.errors());
}

test('a failed mutation RPC surfaces a dismissible notice on the strip', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  await window.route('**/gridwell.v1.Gridwell/PlaceTile', (r: any) => r.abort());
  await gw.dragTileCell(cx, cy, cx + 1, cy);

  await expect
    .poll(async () => {
      const e = await errors(window);
      return e.notices.find((n: any) => n.source === 'rpc:PlaceTile')?.severity ?? null;
    }, { timeout: 10_000 })
    .toBe('error');

  // Every pane ends at or above the strip's top edge, so no WebContentsView
  // bound to a pane rect can cover a notice.
  const e = await errors(window);
  expect(e.stripH).toBeGreaterThan(0);
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  for (const p of panes) {
    expect(p.y + p.h, `pane ${p.id} must end above the notice strip`)
      .toBeLessThanOrEqual(e.stripTop + 0.5);
  }

  // Server state never moved: the optimistic ghost snapped back.
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx, cy), 'tile still at origin').toBeTruthy();

  await window.unroute('**/gridwell.v1.Gridwell/PlaceTile');

  // Clicking the row dismisses it, and the layout returns to full height.
  await gw.clickScreen(200, e.stripTop + 5);
  await expect
    .poll(async () => (await errors(window)).stripH)
    .toBe(0);
});

// A one-shot failure must leave the strip on its own once its source goes quiet
// for errsurface.ExpireAfter, with no user gesture. The sticky exemption is
// pinned by the errsurface unit tests; this proves the live wiring fires.
test('a one-shot notice expires off the strip once its source goes quiet', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx, cy), 'markdown tile created').toBeTruthy();

  // One failed mutation, then the route comes back: the one-shot shape.
  await window.route('**/gridwell.v1.Gridwell/PlaceTile', (r: any) => r.abort());
  await gw.dragTileCell(cx, cy, cx + 1, cy);
  await expect
    .poll(async () => (await errors(window)).notices.some((n: any) => n.source === 'rpc:PlaceTile'))
    .toBe(true);
  await window.unroute('**/gridwell.v1.Gridwell/PlaceTile');

  // No click and no dismiss. ExpireAfter is 10s, so poll well past it.
  await expect
    .poll(async () => (await errors(window)).stripH, { timeout: 20_000, intervals: [1_000] })
    .toBe(0);
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
  const winH = await window.evaluate(() => globalThis.innerHeight);
  const bottom = Math.max(...panes.map((p: any) => p.y + p.h));
  // Panes reclaim the strip's height and nothing more: the bar's band stays
  // reserved below them.
  expect(bottom, 'panes reclaim the reserved strip height').toBe(bar.top);
  expect(bar.top + bar.height, 'the band now reaches the window bottom').toBe(winH);
});

test('a rejected text save surfaces and reconciles instead of lingering as saved', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  await gw.descendCell(cx, cy);
  await gw.typeText('saved-content');
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('saved-content');

  // Rejected bytes that keep rendering as saved are what "it just disappeared"
  // looks like.
  await gw.descendCell(cx, cy);
  // A real Connect rejection body, which the client classifies as
  // OutcomeRejected. An abort is the other contract, kept and retried
  // (web-outage.spec.ts), and would invert this spec's assertion.
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

  // The server's body is untouched, and the cache dropped the rejected bytes.
  const body = await gw.getTileContent(created.id);
  expect(body).toContain('saved-content');
  expect(body).not.toContain('rejected-suffix');
});

// Unhandled, a live url tile's did-fail-load leaves the native view blank with
// no signal. A real net error must reach the wasm errsurface over gw:error,
// attributed to 'electron:webview' because no RPC is involved.
test('an unreachable live URL tile surfaces a did-fail-load notice from the Electron layer', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');

  // The url swatch's modal descends straight into a live url tile. Port 9,
  // discard, has nothing listening, so the connection is refused at once rather
  // than hanging on a timeout.
  await gw.clickMenuSwatch('url');
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

// The shared body of the two framing-writeback failure specs. It reframes
// inside a well with SetFraming failing in the shape `route` decides.
async function setupWellReframe(
  gw: any,
  window: any,
  route: (r: any) => void,
): Promise<{ parentGrid: string; wellID: string; sig0: Record<string, string> }> {
  await gw.enterPlugin('home');
  const parentGrid = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);
  await gw.openMenu();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // Gesture-free: a focus click would refetch the grid it lands on, healing the
  // divergence this spec observes.
  const sig0 = await window.evaluate(
    (gid: string) => (window as any).__gridwellTest.gridSigs(gid),
    parentGrid,
  );
  const wellID = Object.keys(sig0)[0];
  expect(wellID, 'the parent grid holds the well').toBeTruthy();

  await window.route('**/gridwell.v1.Gridwell/SetFraming', route);

  // A synthetic wheel under xvfb can be dropped, and a lost gesture leaves the
  // settle persister nothing to persist: this spec's flake row in
  // docs/flake-ledger.md. The pane's own framing is the delivery ack.
  const z0 = (await gw.focused()).zoom;
  let reframed = false;
  for (let attempt = 0; attempt < 5 && !reframed; attempt++) {
    await gw.wheelAtFocusedCenter(-300);
    try {
      await expect.poll(async () => (await gw.focused()).zoom, { timeout: 2_000 }).not.toBe(z0);
      reframed = true;
    } catch {
      // The wheel was lost before the app; send again.
    }
  }
  if (!reframed) throw new Error('the wheel reframe never landed after 5 sends');
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);

  // The settle persister must post, so the notice below can only fail for
  // far-end reasons.
  await expect
    .poll(
      () => window.evaluate(() => (window as any).__gridwellTest.persistPosts().SetFraming ?? 0),
      { message: 'the settle persister posts SetFraming for the changed framing', timeout: 10_000 },
    )
    .toBeGreaterThan(0);
  await expect
    .poll(async () => {
      const e = await errors(window);
      return (e.notices ?? []).some((n: any) => n.source === 'rpc:SetFraming');
    }, { timeout: 10_000 })
    .toBe(true);

  return { parentGrid, wellID, sig0 };
}

test('a framing writeback REJECTED by the server rolls the optimistic patch back (issue #156)', async ({
  gw,
  window,
}) => {
  // persistFraming patches the cache before posting, so a rejected write must
  // roll back or a sibling pane's preview shows framing the server refused. The
  // rejection is a real Connect error body (OutcomeRejected); a network abort
  // is the other contract, pinned below.
  const { parentGrid, wellID, sig0 } = await setupWellReframe(gw, window, (r: any) =>
    r.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({ code: 'invalid_argument', message: 'e2e: framing write refused' }),
    }),
  );

  // Without the rollback the patched framing sits in the cache until an
  // unrelated gesture refetches.
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
  await window.unroute('**/gridwell.v1.Gridwell/SetFraming');
});

test('a framing writeback lost to TRANSPORT keeps the patch (2026-08-14)', async ({
  gw,
  window,
}) => {
  // An abort means the server never spoke, and the patched framing is the only
  // copy of the user's settled viewport, so rolling it back would lose it. The
  // patch stays and the write parks; web-outage.spec.ts proves the drain.
  const { parentGrid, wellID, sig0 } = await setupWellReframe(gw, window, (r: any) => r.abort());

  // Repeatedly, since a late rollback is the bug.
  for (let i = 0; i < 5; i++) {
    const sigs = await window.evaluate(
      (gid: string) => (window as any).__gridwellTest.gridSigs(gid),
      parentGrid,
    );
    expect(sigs[wellID], 'the transport-failed patch must stay').not.toBe(sig0[wellID]);
    await window.waitForTimeout(300);
  }
  await window.unroute('**/gridwell.v1.Gridwell/SetFraming');
});
