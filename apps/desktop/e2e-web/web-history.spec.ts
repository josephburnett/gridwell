import { test, expect } from './fixtures';

// Structural navigation, meaning a descend, ascend, or portal, pushes a browser
// history entry; framing, meaning a pan or zoom, replaces one. The back button
// therefore traverses descents and ascents and never pan positions, and each
// restored place shows the framing it was left at, which the settle persister
// made server truth. This runs in a real browser because history and popstate
// are a surface the Electron specs do not exercise.

test('back ascends a descent; forward re-descends; pans never make entries', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  await gw.descendCell(cx, cy);
  const child = await gw.focused();
  expect(child.gridID, 'descended into the door').not.toBe(home.gridID);

  // Pan and zoom are framing, so they must create no history entries.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const framed = await gw.focused();
  expect(framed.zoom, 'the reframe took').not.toBeCloseTo(1.0, 2);
  // Let the debounced url write settle so the entry state is current.
  await window.waitForTimeout(400);

  // One back press ascends past the whole pan and zoom excursion.
  await window.evaluate(() => history.back());
  await expect
    .poll(async () => (await gw.focused()).gridID, { timeout: 10_000 })
    .toBe(home.gridID);
  expect((await gw.focused()).path, 'back landed at the parent, path empty').toEqual([]);

  // Forward re-descends into the door and restores the framing it was left at.
  await window.evaluate(() => history.forward());
  await expect
    .poll(async () => (await gw.focused()).gridID, { timeout: 10_000 })
    .toBe(child.gridID);
  const restored = await gw.focused();
  expect(restored.zoom, 'forward restored the framing we left').toBeCloseTo(framed.zoom, 1);
  expect(restored.cx, 'center x restored').toBeCloseTo(framed.cx, 1);
  expect(restored.cy, 'center y restored').toBeCloseTo(framed.cy, 1);
});
