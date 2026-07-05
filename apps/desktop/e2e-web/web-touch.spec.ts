import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';
import { longPressDrag, pinch, twoFingerTap } from './touch';

// Crosses the touch seam (client/touchgest → synthetic mouse/wheel events →
// the untouched gesture engine): REAL TouchEvents injected over CDP must
// drive the canvas exactly as a finger on an iPhone would. Each gesture is
// verified against pane/server state, never pixels.

test('touch: tap enters a plugin and descends', async ({ gw, window }) => {
  const launcher = await window.evaluate(() => (window as any).__gridwellTest.launcher());
  const pl = launcher.find((l: any) => l.label === 'e2e');
  expect(pl, 'launcher tile present').toBeTruthy();
  await window.touchscreen.tap(pl.x, pl.y);
  await gw.waitIdle();
  const f = await gw.focused();
  expect(f.anchor, 'tap entered the plugin').not.toBe('');
});

test('touch: pinch zooms the focused grid at the pinch midpoint', async ({ gw, window }) => {
  await gw.enterPlugin('e2e');
  let f = await gw.focused();
  const z0 = f.zoom;
  const center = { x: f.x + f.w / 2, y: f.y + f.h / 2 };

  await pinch(window, center, 40, 160); // spread → zoom in
  await gw.waitIdle();
  f = await gw.focused();
  expect(f.zoom, 'spread pinch zooms in').toBeGreaterThan(z0);

  const z1 = f.zoom;
  await pinch(window, center, 160, 40); // close → zoom out
  await gw.waitIdle();
  f = await gw.focused();
  expect(f.zoom, 'closing pinch zooms out').toBeLessThan(z1);
});

test('touch: long-press-drag from the right edge splits the pane', async ({ gw, window }) => {
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const before = (await gw.panes()).length;
  const y = f.y + f.h / 2;
  // Same geometry as driver.splitFocusedPaneVertical, but by finger: hold in
  // the right-edge band (becomes the right button), drag to mid-pane.
  await longPressDrag(window, { x: f.x + f.w - 5, y }, { x: f.x + f.w * 0.45, y });
  await gw.waitIdle();
  expect((await gw.panes()).length, 'long-press-drag split the pane').toBe(before + 1);
});

test('touch: drag moves a tile; two-finger tap ascends a descent', async ({ gw, window }) => {
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  // One-finger drag = left drag: move the tile one cell right. Cell centers
  // come from the same hook the mouse specs use.
  const fromPt = await gw.cellCenter(f.id, cx, cy);
  const toPt = await gw.cellCenter(f.id, cx + 1, cy);
  const s = await window.context().newCDPSession(window);
  await s.send('Input.dispatchTouchEvent', {
    type: 'touchStart',
    touchPoints: [{ x: fromPt.x, y: fromPt.y }],
  });
  const steps = 8;
  for (let i = 1; i <= steps; i++) {
    await s.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: [
        {
          x: fromPt.x + ((toPt.x - fromPt.x) * i) / steps,
          y: fromPt.y + ((toPt.y - fromPt.y) * i) / steps,
        },
      ],
    });
  }
  await s.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await s.detach();
  await gw.waitIdle();
  expect(
    tileAt(await gw.getGrid(f.gridID), 'text', cx + 1, cy),
    'one-finger drag moved the tile on the server',
  ).toBeTruthy();

  // Tap descends into it; two-finger tap ascends back out.
  await window.touchscreen.tap(toPt.x, toPt.y);
  await gw.waitIdle();
  let p = await gw.focused();
  expect(p.textFocus, 'tap descended into the text tile').not.toBe('');
  await twoFingerTap(window, { x: p.x + p.w / 2, y: p.y + p.h / 2 });
  await gw.waitIdle();
  p = await gw.focused();
  expect(p.textFocus, 'two-finger tap ascended').toBe('');
});
