import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';

// The wheel zooms the grid the pane shows wherever the cursor is, a door
// included; alt + wheel zooms the grid inside the door under the cursor, its
// framing on the door's row, and over anything else does nothing
// (gesture.ClassifyWheel). The spec crosses the whole seam: real wheel events
// with altKey, the classifier, zoomtrans.DoorWheelView, the settle
// persister's SetFraming, and the rows read back through GetGrid.

const view = (p: { cx: number; cy: number; zoom: number }) => ({ cx: p.cx, cy: p.cy, zoom: p.zoom });

async function altWheel(window: any, dy: number): Promise<void> {
  await window.keyboard.down('Alt');
  await window.mouse.wheel(0, dy);
  await window.keyboard.up('Alt');
}

test('a wheel over a door zooms the pane and leaves the door row as it was', async ({ gw, window }) => {
  const c = await gw.cadences();
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  await gw.waitIdle();
  await settle(window, c.framingSaveMs);
  const before = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  expect(before, 'door created').toBeTruthy();

  const z0 = (await gw.focused()).zoom;
  const pt = await gw.cellCenter(home.id, cx, cy);
  await window.mouse.move(pt.x, pt.y);
  await expect
    .poll(async () => {
      await window.mouse.wheel(0, -120);
      return (await gw.focused()).zoom;
    })
    .toBeGreaterThan(z0);
  await gw.waitIdle();
  await settle(window, c.framingSaveMs);
  const after = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  expect(JSON.stringify(after), 'the door row is byte-identical').toBe(JSON.stringify(before));
});

test('alt + wheel over a door zooms its inside and leaves the pane where it was', async ({ gw, window }) => {
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const door = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  expect(door, 'door created').toBeTruthy();
  expect(Number(door.viewZoom ?? 0), 'a fresh door has no stored view yet').toBe(0);
  await gw.waitIdle();
  const paneBefore = view(await gw.focused());

  // The door's stored zoom rises above the unvisited default of 0.125, both
  // ways of the wheel are the door's, and the pane's view is untouched.
  const pt = await gw.cellCenter(home.id, cx, cy);
  await window.mouse.move(pt.x, pt.y);
  const doorZoom = async () =>
    Number((tileAt(await gw.getGrid(home.gridID), 'door', cx, cy) as { viewZoom?: number | string })?.viewZoom ?? 0);
  for (let i = 0; i < 6; i++) await altWheel(window, -120);
  await expect.poll(doorZoom, { timeout: 10_000 }).toBeGreaterThan(0.125);
  const zoomedIn = await doorZoom();
  for (let i = 0; i < 3; i++) await altWheel(window, 120);
  await expect.poll(doorZoom, { timeout: 10_000 }).toBeLessThan(zoomedIn);
  await gw.waitIdle();
  expect(view(await gw.focused()), "the pane's view did not move").toEqual(paneBefore);
});

test('alt + wheel over the background changes nothing', async ({ gw, window }) => {
  const c = await gw.cadences();
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  await gw.waitIdle();
  await settle(window, c.framingSaveMs);
  const gridBefore = JSON.stringify(await gw.getGrid(home.gridID));
  const paneBefore = view(await gw.focused());

  const empty = await gw.cellCenter(home.id, cx + 3, cy + 2);
  await window.mouse.move(empty.x, empty.y);
  for (let i = 0; i < 6; i++) await altWheel(window, -120);
  await gw.waitIdle();
  await settle(window, c.framingSaveMs);
  expect(view(await gw.focused()), "the pane's view did not move").toEqual(paneBefore);
  expect(JSON.stringify(await gw.getGrid(home.gridID)), 'every row is byte-identical').toBe(gridBefore);

  // The same alt wheels reach the canvas: over the door they zoom its inside.
  const pt = await gw.cellCenter(home.id, cx, cy);
  await window.mouse.move(pt.x, pt.y);
  await expect
    .poll(
      async () => {
        await altWheel(window, -120);
        const t = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy) as { viewZoom?: number | string };
        return Number(t?.viewZoom ?? 0);
      },
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);
});

// The door's zoom is cursor-anchored: the child point under the cursor stays
// under it, so a burst of notches with the cursor off-center drifts the stored
// view toward the cursor. The framing is a float center (view_cx, view_cy) so
// that sub-cell drift survives to the store.
test('an off-center alt + wheel burst drifts the door view toward the cursor (#219)', async ({
  gw,
  window,
}) => {
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const before = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  expect(before, 'door created').toBeTruthy();
  // Nothing is stored yet: a never-visited door frames the center of its own
  // footprint (zoomtrans.EffectiveCenter), and that is the center the drift has
  // to beat.
  expect(Number(before.viewZoom ?? 0), 'a fresh door has no stored framing').toBe(0);
  const vx0 = Number(before.w ?? 1) / 2;
  const vy0 = Number(before.h ?? 1) / 2;

  // The cursor sits near the door's bottom-right corner, inside the tile, so
  // the view center must chase it down and right.
  const pt = await gw.cellCenter((await gw.focused()).id, cx, cy);
  await window.mouse.move(pt.x + 18, pt.y + 18);
  for (let i = 0; i < 10; i++) await altWheel(window, -120);
  await expect
    .poll(
      async () => {
        const t = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy);
        return Number(t?.viewCx ?? 0) + Number(t?.viewCy ?? 0);
      },
      { message: 'the stored center must drift toward the cursor', timeout: 10_000 },
    )
    .toBeGreaterThan(vx0 + vy0);
});
