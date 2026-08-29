import { test, expect } from './fixtures';

// Crosses the browser-history seam (issue #194): structural navigation
// (descend / ascend / portal) pushes a history entry, framing (pan / zoom)
// replaces — so the back button traverses descend/ascend, never pan
// positions, and each restored place shows the framing you left (issue
// #190's settle persister makes that server truth). Driven in the real
// browser because history/popstate is exactly the browser-integration
// surface Electron specs don't exercise.

test('back ascends a descent; forward re-descends; pans never make entries', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  const child = await gw.focused();
  expect(child.gridID, 'descended into the well').not.toBe(home.gridID);

  // Pan and zoom INSIDE the well — framing only, must not create entries.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const framed = await gw.focused();
  expect(framed.zoom, 'the reframe took').not.toBeCloseTo(1.0, 2);
  // Let the debounced URL write settle so the entry state is current.
  await window.waitForTimeout(400);

  // BACK: one press ascends past the whole pan/zoom excursion.
  await window.evaluate(() => history.back());
  await expect
    .poll(async () => (await gw.focused()).gridID, { timeout: 10_000 })
    .toBe(home.gridID);
  expect((await gw.focused()).path, 'back landed at the parent, path empty').toEqual([]);

  // FORWARD: re-descends into the well, restoring the framing we left
  // (server truth via the settle persister — this is the "go back to where
  // I was" half of the ask).
  await window.evaluate(() => history.forward());
  await expect
    .poll(async () => (await gw.focused()).gridID, { timeout: 10_000 })
    .toBe(child.gridID);
  const restored = await gw.focused();
  expect(restored.zoom, 'forward restored the framing we left').toBeCloseTo(framed.zoom, 1);
  expect(restored.cx, 'center x restored').toBeCloseTo(framed.cx, 1);
  expect(restored.cy, 'center y restored').toBeCloseTo(framed.cy, 1);
});
