import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #210: hovering a well and wheeling zooms the grid IN the well — its
// stored view_zoom preview framing, a server fact — not the grid the pane
// shows. Empty space is the escape hatch: wheel there still zooms the pane.
// The spec crosses the whole seam: real wheel events → classifier →
// zoomtrans.WellWheelView → per-notch cache patch → the settle persister's
// SetWellView → server truth read back through GetGrid.

test('wheel over a well zooms the well; over empty space, the pane (#210)', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(well, 'well created').toBeTruthy();
  expect(Number(well.viewZoom ?? 0), 'a fresh well has no stored view yet').toBe(0);

  const paneZoomBefore = (await gw.focused()).zoom;

  // Wheel IN over the well's center: the WELL's persisted viewZoom rises
  // above the unvisited default (0.125); the pane's own zoom is untouched.
  const pt = await gw.cellCenter((await gw.focused()).id, cx, cy);
  await window.mouse.move(pt.x, pt.y);
  for (let i = 0; i < 6; i++) {
    await window.mouse.wheel(0, -120);
  }
  await expect
    .poll(
      async () => {
        const t = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy);
        return Number((t as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0.125);
  expect((await gw.focused()).zoom, 'the pane zoom did not move').toBeCloseTo(paneZoomBefore, 5);

  // Wheel over EMPTY SPACE (a far corner cell, no tile): the pane zooms,
  // the well's stored view stays as the hover-zoom left it.
  const t1 = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  const wellZoomAfter = Number(t1.viewZoom ?? 0);
  const f = await gw.focused();
  await window.mouse.move(f.x + f.w * 0.1, f.y + f.h * 0.9);
  await window.mouse.wheel(0, -120);
  await expect.poll(async () => (await gw.focused()).zoom).not.toBeCloseTo(paneZoomBefore, 5);
  await gw.waitIdle();
  const t2 = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(Number(t2.viewZoom ?? 0), 'the well view survived the pane zoom').toBeCloseTo(
    wellZoomAfter,
    5,
  );
});

// Issue #219: the well wheel-zoom is cursor-ANCHORED — the child point
// under the cursor stays under the cursor, so a burst of notches with the
// cursor off-center DRIFTS the stored view toward the cursor (zooming as
// small navigation). Before the fix each notch quantized the origin to
// integer cells, the sub-cell drift rounded away, and the persisted origin
// never moved.
test('an off-center wheel burst drifts the well view toward the cursor (#219)', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const before = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(before, 'well created').toBeTruthy();
  const vx0 = Number(before.viewX ?? 0);
  const vy0 = Number(before.viewY ?? 0);

  // Cursor near the well's bottom-right corner (inside the tile), then a
  // long zoom-in burst: the view center must chase the cursor's child
  // point, moving the persisted origin down-right.
  const pt = await gw.cellCenter((await gw.focused()).id, cx, cy);
  await window.mouse.move(pt.x + 18, pt.y + 18);
  for (let i = 0; i < 10; i++) {
    await window.mouse.wheel(0, -120);
  }
  await expect
    .poll(
      async () => {
        const t = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy);
        return Number(t?.viewX ?? 0) + Number(t?.viewY ?? 0);
      },
      { message: 'the stored origin must drift toward the cursor', timeout: 10_000 },
    )
    .toBeGreaterThan(vx0 + vy0);
});
