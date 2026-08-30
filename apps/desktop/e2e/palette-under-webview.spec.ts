import { test, expect } from './fixtures';
import { PARK_COORD } from '../src/main/viewutil';

// The palette must never appear under a live WebContentsView. Two registry
// contracts in webviews.ts keep a parked view parked:
//
//   1. A bounds change while hidden must not move the view. syncURLViews calls
//      setBounds() every frame, and while the palette or a drag gesture has the
//      view parked, a new rect must only update the stored bounds so the un-park
//      lands at the new position. Calling view.setBounds there would lift the
//      view out of its park over the canvas overlay, and the next
//      setHidden(true) would no-op, since e.hidden is already true.
//
//   2. A fresh entry must not start visible. A view placed while the palette is
//      open would otherwise land on top of it for one IPC round trip, until the
//      following setHidden(true) arrived. The renderer's verdict for that frame
//      rides PlaceArgs.hidden; the registry keeps no global "something is
//      parked" state.
//
// Both tests run entirely in the main process through electronApp.evaluate, the
// only way to exercise a live WebContentsView, since it is off the main
// webContents. They call registry methods directly and assert physical view
// positions through viewBoundsFor() against PARK_COORD, rather than the stored
// hidden flag, so a view lifted out of its park is observable at the seam.

// PARK_COORD is the registry's own park position. viewutil.ts is electron-free,
// so the spec reads the one owner instead of carrying a copy.

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
    await reg.place(paneId, 1, args.dataURL, initialBounds);

    // Wait for the view to appear in webContents, after the preload and data url
    // load.
    const deadline = Date.now() + 8_000;
    let found = false;
    while (!found && Date.now() < deadline) {
      found = webContents.getAllWebContents().some((w: any) => w.getURL().includes(args.marker));
      if (!found) await new Promise<void>((res) => setTimeout(res, 50));
    }
    if (!found) throw new Error('live view webContents not found after place()');

    // Simulate an open palette or a running gesture: park the view.
    reg.setHidden(paneId, true, true);
    const boundsWhileHidden = reg.viewBoundsFor(paneId);

    // The next frame's rect arrives while the view is still parked, because the
    // pane split or the window resized. The view must stay at park coords.
    reg.setBounds(paneId, newBounds);
    const boundsAfterResize = reg.viewBoundsFor(paneId);

    // Simulate the palette closing: un-park. The view must move to newBounds
    // rather than initialBounds, which confirms e.bounds updated while hidden.
    reg.setHidden(paneId, false, true);
    const boundsAfterUnpark = reg.viewBoundsFor(paneId);

    await reg.remove(paneId);

    return { boundsWhileHidden, boundsAfterResize, boundsAfterUnpark, newBounds };
  }, { dataURL: 'data:text/html,<meta charset=utf8>parktest', marker: 'parktest' });

  // After setHidden(true), the view must be at PARK_COORD.
  expect(result.boundsWhileHidden?.x, 'setHidden(true) parks the view at PARK_COORD').toBe(PARK_COORD);

  // After setBounds() while still hidden the view must remain parked; lifting it
  // would put it at (200, 150), on top of the palette.
  expect(
    result.boundsAfterResize?.x,
    'setBounds() while hidden must NOT lift the view out of park',
  ).toBe(PARK_COORD);

  // After setHidden(false) the view must be at the new bounds, not the old ones,
  // proving e.bounds updated while hidden.
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

    // Place a brand-new view with the renderer's overlay-is-open verdict,
    // PlaceArgs.hidden true. It must start parked, not at its bounds.
    await reg.place(paneId, 2, args.dataURL, bounds, 0, '', false, true);
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
