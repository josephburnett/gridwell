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

  // Sever MoveTile at the transport and drag the tile one cell.
  await window.route('**/gridwell.v1.Gridwell/MoveTile', (r: any) => r.abort());
  await gw.dragTileCell(cx, cy, cx + 1, cy);

  // The failure lands on the strip, as an error, attributed to the RPC.
  await expect
    .poll(async () => {
      const e = await errors(window);
      return e.notices.find((n: any) => n.source === 'rpc:MoveTile')?.severity ?? null;
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

  await window.unroute('**/gridwell.v1.Gridwell/MoveTile');

  // Clicking the row dismisses it and returns the layout to full height.
  await gw.clickScreen(200, e.stripTop + 5);
  await expect
    .poll(async () => (await errors(window)).stripH)
    .toBe(0);
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
  await gw.rightClickPlus();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('saved-content');

  // Re-descend, sever UpdateText, and type. The debounced save must fail
  // VISIBLY: before issue #45 this was the literal "it just disappeared"
  // mechanism — the rejected bytes kept rendering as saved with no signal.
  await gw.descendCell(cx, cy);
  await window.route('**/gridwell.v1.Gridwell/UpdateText', (r: any) => r.abort());
  await gw.typeText(' rejected-suffix');
  await expect
    .poll(async () => {
      const e = await errors(window);
      return e.notices.some((n: any) => n.source === 'rpc:UpdateText');
    }, { timeout: 15_000 })
    .toBe(true);
  await window.unroute('**/gridwell.v1.Gridwell/UpdateText');

  // Reconcile: the server's body is untouched — and the client did not keep
  // claiming otherwise (the strip said so; the cache dropped the bytes).
  const body = await gw.getTileContent(created.id);
  expect(body).toContain('saved-content');
  expect(body).not.toContain('rejected-suffix');
});
