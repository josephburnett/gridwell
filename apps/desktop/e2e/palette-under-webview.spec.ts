import { test, expect } from './fixtures';
import { PARK_COORD } from '../src/main/viewutil';

// The palette must never appear under a live WebContentsView. Two registry
// contracts in webviews.ts keep a parked view parked:
//
//   1. A bounds change while hidden updates only the stored bounds, so the
//      un-park lands at the new position. Calling view.setBounds there lifts
//      the view over the canvas overlay, and the next setHidden(true) no-ops
//      because e.hidden is already true.
//
//   2. A fresh entry placed while the palette is open starts parked, on the
//      renderer's verdict in PlaceArgs.hidden. The registry keeps no global
//      "something is parked" state.
//
// Both tests run in the main process through electronApp.evaluate, the only way
// to reach a live WebContentsView, and read physical positions through
// viewBoundsFor() against PARK_COORD rather than the stored hidden flag, so a
// view lifted out of its park is observable. PARK_COORD comes from the
// electron-free viewutil.ts, the one owner of the park position.

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

    await reg.place(paneId, 1, args.dataURL, initialBounds);

    // The view appears in webContents only after the preload and data url
    // load.
    const deadline = Date.now() + 8_000;
    let found = false;
    while (!found && Date.now() < deadline) {
      found = webContents.getAllWebContents().some((w: any) => w.getURL().includes(args.marker));
      if (!found) await new Promise<void>((res) => setTimeout(res, 50));
    }
    if (!found) throw new Error('live view webContents not found after place()');

    // An open palette or a running gesture parks the view, once the face its
    // pane shows meanwhile is taken.
    reg.setHidden(paneId, true, true);
    const parkDeadline = Date.now() + 8_000;
    while (reg.viewBoundsFor(paneId)?.x !== args.parkCoord && Date.now() < parkDeadline) {
      await new Promise<void>((res) => setTimeout(res, 20));
    }
    const boundsWhileHidden = reg.viewBoundsFor(paneId);

    // A new rect arrives while the view is still parked, because the pane split
    // or the window resized.
    reg.setBounds(paneId, newBounds);
    const boundsAfterResize = reg.viewBoundsFor(paneId);

    // The palette closes and the view un-parks. Landing at newBounds is what
    // shows e.bounds updated while hidden.
    reg.setHidden(paneId, false, true);
    const boundsAfterUnpark = reg.viewBoundsFor(paneId);

    await reg.remove(paneId);

    return { boundsWhileHidden, boundsAfterResize, boundsAfterUnpark, newBounds };
  }, { dataURL: 'data:text/html,<meta charset=utf8>parktest', marker: 'parktest', parkCoord: PARK_COORD });

  expect(result.boundsWhileHidden?.x, 'setHidden(true) parks the view at PARK_COORD').toBe(PARK_COORD);

  // Lifting the view here would put it at (200, 150), on top of the palette.
  expect(
    result.boundsAfterResize?.x,
    'setBounds() while hidden must NOT lift the view out of park',
  ).toBe(PARK_COORD);

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

    // The last argument is PlaceArgs.hidden: the renderer's overlay-is-open
    // verdict for this frame.
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

  expect(
    result.boundsAfterPlace?.x,
    'a new view placed with hidden=true must start at PARK_COORD',
  ).toBe(PARK_COORD);

  expect(result.boundsAfterUnpark?.x, 'after un-park, new view is at visible bounds').toBe(result.bounds.x);
  expect(result.boundsAfterUnpark?.y).toBe(result.bounds.y);
});
