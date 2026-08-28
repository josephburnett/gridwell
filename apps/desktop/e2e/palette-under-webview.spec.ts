import { test, expect } from './fixtures';
import { PARK_COORD } from '../src/main/viewutil';

// Regression guard for mechanism A of issue #33: "palette appears under a live
// WebContentsView." Two registry contracts in webviews.ts keep a parked view
// parked:
//
//   1. A bounds change while hidden must not move the view. syncURLViews calls
//      setBounds() every frame; while the palette (or a drag gesture) has the
//      view parked, a new rect must only update the stored bounds so that the
//      un-park lands at the NEW position — never view.setBounds, which would
//      physically lift the view out of its park over the canvas overlay, and
//      the next setHidden(true) would no-op (e.hidden already true).
//      (This used to be tested through place()'s reuse path, which was
//      unreachable from the wasm and was deleted; setBounds is the path the
//      renderer actually takes.)
//
//   2. New view creation: a fresh entry always started with hidden:false, so
//      a view placed while the palette was open landed on top of it for one IPC
//      round-trip before the following setHidden(true) arrived. The renderer's
//      verdict for THIS frame now rides PlaceArgs.hidden (the registry keeps no
//      global "something is parked" state — that ranged over a Go map and could
//      park a new view at random).
//
// Both tests run entirely in the main process via electronApp.evaluate (the only
// way to exercise a live WebContentsView — it's off the main webContents). They
// directly call registry methods, asserting actual physical view positions via
// viewBoundsFor() — PARK_COORD from viewutil.ts — rather than the stored hidden
// flag, so a view lifted out of its park is observable at the seam.

// PARK_COORD is the registry's own park position (viewutil.ts is electron-free,
// so the spec reads the one owner instead of carrying a copy).

test('setBounds() while hidden keeps the view parked and un-parks at the new bounds', async ({
  electronApp,
  window,
}) => {
  await window.title();

  const result = await electronApp.evaluate(async ({ webContents }, args) => {
    const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
    if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');

    const paneId = 'e2e-park-resize';
    const initialBounds = { x: 100, y: 100, width: 400, height: 300 };
    const newBounds = { x: 200, y: 150, width: 500, height: 350 };

    // Place a live view at its initial position.
    await reg.place(paneId, 1, 'obj-park-resize', args.dataURL, initialBounds);

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

    // The next frame's rect arrives while the view is still parked (the pane
    // was split, the window resized, …). The view must remain at park coords.
    reg.setBounds(paneId, newBounds);
    const boundsAfterResize = reg.viewBoundsFor(paneId);

    // Simulate "palette closed": un-park. The view must move to newBounds, not
    // initialBounds — confirming e.bounds was updated while hidden.
    reg.setHidden(paneId, false, true);
    const boundsAfterUnpark = reg.viewBoundsFor(paneId);

    await reg.remove(paneId);

    return { boundsWhileHidden, boundsAfterResize, boundsAfterUnpark, newBounds };
  }, { dataURL: 'data:text/html,<meta charset=utf8>parktest', marker: 'parktest' });

  // After setHidden(true), the view must be at PARK_COORD.
  expect(result.boundsWhileHidden?.x, 'setHidden(true) parks the view at PARK_COORD').toBe(PARK_COORD);

  // After setBounds() while still hidden, the view must remain parked — a
  // regression would put it at (200, 150), on top of the palette.
  expect(
    result.boundsAfterResize?.x,
    'setBounds() while hidden must NOT lift the view out of park',
  ).toBe(PARK_COORD);

  // After setHidden(false), the view must be at the NEW bounds (not the old ones),
  // proving e.bounds was correctly updated while hidden.
  expect(
    result.boundsAfterUnpark?.x,
    'after un-park, view must be at the NEW bounds supplied while hidden',
  ).toBe(result.newBounds.x);
  expect(result.boundsAfterUnpark?.y).toBe(result.newBounds.y);
});

test('a new view placed with hidden=true starts parked (new-view-path fix)', async ({
  electronApp,
  window,
}) => {
  await window.title();

  const result = await electronApp.evaluate(async ({ webContents }, args) => {
    const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
    if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');

    const paneId = 'e2e-new-while-hidden';
    const bounds = { x: 100, y: 100, width: 400, height: 300 };

    // Place a brand-new view with the renderer's "overlay is open" verdict
    // (PlaceArgs.hidden = true). It must start parked, not at its bounds.
    await reg.place(paneId, 2, 'obj-new-while-hidden', args.dataURL, bounds, 0, '', false, true);
    const boundsAfterPlace = reg.viewBoundsFor(paneId);
    const dLoad = Date.now() + 8_000;
    while (!webContents.getAllWebContents().some((w: any) => w.getURL().includes(args.marker)) && Date.now() < dLoad) {
      await new Promise<void>((res) => setTimeout(res, 50));
    }

    // The next syncURLViews frame un-parks it: it moves to its visible bounds.
    reg.setHidden(paneId, false, true);
    const boundsAfterUnpark = reg.viewBoundsFor(paneId);

    await reg.remove(paneId);

    return { boundsAfterPlace, boundsAfterUnpark, bounds };
  }, { dataURL: 'data:text/html,<meta charset=utf8>newviewtest', marker: 'newviewtest' });

  // The new view must start parked, not at its visible bounds.
  expect(
    result.boundsAfterPlace?.x,
    'a new view placed with hidden=true must start at PARK_COORD',
  ).toBe(PARK_COORD);

  // After un-parking it moves to its visible bounds.
  expect(result.boundsAfterUnpark?.x, 'after un-park, new view is at visible bounds').toBe(result.bounds.x);
  expect(result.boundsAfterUnpark?.y).toBe(result.bounds.y);
});
