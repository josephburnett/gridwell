import { test, expect } from './fixtures';

// Regression guard for mechanism A of issue #33: "palette appears under a live
// WebContentsView." The fix has two parts in webviews.ts:
//
//   1. place() reuse-path bug: calling place() with new bounds on a parked
//      (hidden=true) entry was physically lifting the view out of its park
//      position because e.view.setBounds(rounded) ran unconditionally. The next
//      setHidden(paneId, true, …) call was then a no-op (e.hidden was already
//      true) so the view stayed on-screen over the canvas overlay.
//
//   2. New view creation: a fresh entry always started with hidden:false, so
//      a view placed while the palette was open landed on top of it for one IPC
//      round-trip before the following setHidden(true) arrived.
//
// Both tests run entirely in the main process via electronApp.evaluate (the only
// way to exercise a live WebContentsView — it's off the main webContents). They
// directly call registry methods, asserting actual physical view positions via
// viewBoundsFor() — PARK_COORD from viewutil.ts — rather than the stored hidden
// flag, so the "place re-asserts over palette" bug is observable at the seam.

// PARK_COORD from viewutil.ts: far enough off any display that a parked view is
// not visible. Inlined here since the e2e can't import from main.
const PARK_COORD = -100000;

test('place() with new bounds while hidden keeps the view parked (reuse-path fix)', async ({
  electronApp,
  window,
}) => {
  await window.title();

  const result = await electronApp.evaluate(async ({ webContents }, args) => {
    const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
    if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');

    const paneId = 'e2e-park-reuse';
    const initialBounds = { x: 100, y: 100, width: 400, height: 300 };
    const newBounds = { x: 200, y: 150, width: 500, height: 350 };

    // Place a live view at its initial position.
    await reg.place(paneId, 1, 'obj-park-reuse', args.dataURL, initialBounds, '');

    // Wait for the view to appear in webContents (the preload + data URL load).
    const deadline = Date.now() + 8_000;
    let found = false;
    while (!found && Date.now() < deadline) {
      found = webContents.getAllWebContents().some((w: any) => w.getURL().includes(args.marker));
      if (!found) await new Promise<void>((res) => setTimeout(res, 50));
    }
    if (!found) throw new Error('live view webContents not found after place()');

    // Simulate "palette/gesture is open": park the view via setHidden.
    reg.setHidden(paneId, true, true);
    const boundsWhileHidden = reg.viewBoundsFor(paneId);

    // Call place() with NEW bounds while the view is still parked. Before the
    // fix this physically moved the view to the new visible position; after the
    // fix the view must remain at park coords.
    await reg.place(paneId, 1, 'obj-park-reuse', args.dataURL, newBounds, '');
    const boundsAfterReplace = reg.viewBoundsFor(paneId);

    // Simulate "palette closed": un-park. The view must move to newBounds, not
    // initialBounds — confirming e.bounds was updated while hidden.
    reg.setHidden(paneId, false, true);
    const boundsAfterUnpark = reg.viewBoundsFor(paneId);

    await reg.remove(paneId);

    return { boundsWhileHidden, boundsAfterReplace, boundsAfterUnpark, newBounds };
  }, { dataURL: 'data:text/html,<meta charset=utf8>parktest', marker: 'parktest' });

  // After setHidden(true), the view must be at PARK_COORD.
  expect(result.boundsWhileHidden?.x, 'setHidden(true) parks the view at PARK_COORD').toBe(PARK_COORD);

  // After place() with new bounds while still hidden, the view must remain parked —
  // this is the regression: before the fix, it would be at (200, 150).
  expect(
    result.boundsAfterReplace?.x,
    'place() with new bounds while hidden must NOT lift the view out of park',
  ).toBe(PARK_COORD);

  // After setHidden(false), the view must be at the NEW bounds (not the old ones),
  // proving e.bounds was correctly updated while hidden.
  expect(
    result.boundsAfterUnpark?.x,
    'after un-park, view must be at the NEW bounds supplied during place()',
  ).toBe(result.newBounds.x);
  expect(result.boundsAfterUnpark?.y).toBe(result.newBounds.y);
});

test('new view placed while _globalHidden=true starts parked (new-view-path fix)', async ({
  electronApp,
  window,
}) => {
  await window.title();

  const result = await electronApp.evaluate(async ({ webContents }, args) => {
    const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
    if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');

    // Use a sentinel pane to drive _globalHidden: place it, park it, then
    // place a NEW view. The new view should inherit the hidden state and start
    // parked rather than landing at its visible bounds.
    const sentinelId = 'e2e-sentinel';
    const newPaneId = 'e2e-new-while-hidden';
    const bounds = { x: 100, y: 100, width: 400, height: 300 };

    // Place the sentinel and park it — this sets _globalHidden=true.
    await reg.place(sentinelId, 1, 'obj-sentinel', args.dataURL, bounds, '');
    const dSentinel = Date.now() + 8_000;
    while (!webContents.getAllWebContents().some((w: any) => w.getURL().includes(args.marker)) && Date.now() < dSentinel) {
      await new Promise<void>((res) => setTimeout(res, 50));
    }
    reg.setHidden(sentinelId, true, false);

    // Now place a brand-new view. With _globalHidden=true it should start parked.
    await reg.place(newPaneId, 2, 'obj-new-while-hidden', args.dataURL, bounds, '');
    const boundsAfterPlace = reg.viewBoundsFor(newPaneId);

    // Restore: un-park the sentinel and the new view, then clean up.
    reg.setHidden(sentinelId, false, false);
    reg.setHidden(newPaneId, false, true);
    const boundsAfterUnpark = reg.viewBoundsFor(newPaneId);

    await reg.remove(sentinelId);
    await reg.remove(newPaneId);

    return { boundsAfterPlace, boundsAfterUnpark, bounds };
  }, { dataURL: 'data:text/html,<meta charset=utf8>newviewtest', marker: 'newviewtest' });

  // The new view must start parked, not at its visible bounds.
  expect(
    result.boundsAfterPlace?.x,
    'a new view placed while _globalHidden=true must start at PARK_COORD',
  ).toBe(PARK_COORD);

  // After un-parking it moves to its visible bounds.
  expect(result.boundsAfterUnpark?.x, 'after un-park, new view is at visible bounds').toBe(result.bounds.x);
  expect(result.boundsAfterUnpark?.y).toBe(result.bounds.y);
});
