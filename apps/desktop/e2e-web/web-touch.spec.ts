import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';
import { longPress, longPressDrag, pinch, twoFingerTap } from './touch';
import { installEchoGate, holdEchoFor } from './echo';

// Crosses the touch seam, from client/touchgest through synthetic mouse and
// wheel events into the unchanged gesture engine: real TouchEvents injected over
// CDP must drive the canvas as a finger on a phone would. Each gesture is
// verified against pane and server state, never pixels.

test('touch: tap opens the + menu and descends through a swatch', async ({ gw, window }) => {
  // Boot already sits at home's root, and every namespace lives on the + menu,
  // so the tap gesture is verified against that portal. The descent pushes a
  // frame for the return trip.
  await gw.sources();
  const before = await gw.focused();
  const menu = await gw.menu();
  await window.touchscreen.tap(menu.plusX, menu.plusY);
  await window.waitForFunction(() => (window as any).__gridwellTest.menu().open, null, {
    timeout: 5_000,
  });
  // The swatch section is folded on every opening, and a finger opens it by
  // tapping the chevron strip, which is a press like any other.
  const folded = await gw.menu();
  expect(folded.toggle.present, 'the folded section offers its chevron').toBe(true);
  expect(folded.items.some((i) => i.isSwatch), 'and hides the swatches').toBe(false);
  await window.touchscreen.tap(
    folded.toggle.x + folded.toggle.w / 2,
    folded.toggle.y + folded.toggle.h / 2,
  );
  await window.waitForFunction(
    () => (window as any).__gridwellTest.menu().toggle.expanded,
    null,
    { timeout: 5_000 },
  );
  const open = await gw.menu();
  const swatch = open.items.find((i) => i.isSwatch && i.label === 'home');
  expect(swatch, 'the home swatch is in the + menu').toBeTruthy();
  await window.touchscreen.tap(swatch!.x + swatch!.w / 2, swatch!.y + swatch!.h / 2);
  await gw.waitIdle();
  const f = await gw.focused();
  expect(f.gridID, "tap descended to home's root").toBe(before.gridID);
  expect(f.placeDepth, 'the tap ran the descent (one frame pushed)').toBe(1);
});

test('touch: pinch zooms the focused grid at the pinch midpoint', async ({ gw, window }) => {
  await gw.enterSource('home');
  let f = await gw.focused();
  const z0 = f.zoom;
  const center = { x: f.x + f.w / 2, y: f.y + f.h / 2 };

  await pinch(window, center, 40, 160); // spread: zoom in
  await gw.waitIdle();
  f = await gw.focused();
  expect(f.zoom, 'spread pinch zooms in').toBeGreaterThan(z0);

  const z1 = f.zoom;
  await pinch(window, center, 160, 40); // close: zoom out
  await gw.waitIdle();
  f = await gw.focused();
  expect(f.zoom, 'closing pinch zooms out').toBeLessThan(z1);
});

test('touch: long-press-drag from the right edge splits the pane', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const before = (await gw.panes()).length;
  const y = f.y + f.h / 2;
  // The same geometry as driver.splitFocusedPaneVertical, by finger. A hold in
  // the right-edge band becomes the right button, then the drag goes mid-pane.
  await longPressDrag(window, { x: f.x + f.w - 5, y }, { x: f.x + f.w * 0.45, y });
  await gw.waitIdle();
  expect((await gw.panes()).length, 'long-press-drag split the pane').toBe(before + 1);
});

test('touch: tapping the previous crumb ascends a text descent (#222)', async ({ gw, window }) => {
  // The ascent gesture is the crumb click, and on touch that is a plain tap on
  // the second-to-last chain crumb. The canvas translation routes it as the left
  // mousedown the bar hit-test acts on.
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const pt = await gw.cellCenter(f.id, cx, cy);
  await window.touchscreen.tap(pt.x, pt.y);
  await gw.waitIdle();
  let p = await gw.focused();
  expect(p.textFocus, 'tap descended into the text tile').not.toBe('');

  const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
  const chain = bar.segments.filter((s: any) => s.kind === 'chain');
  const seg = chain[chain.length - 2];
  await window.touchscreen.tap(seg.x + seg.w / 2, bar.top + bar.height / 2);
  await gw.waitIdle();
  p = await gw.focused();
  expect(p.textFocus, 'crumb tap ascended').toBe('');
});

test('touch: drag moves a tile; two-finger tap ascends a descent', async ({ gw, window }) => {
  await installEchoGate(window);
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();

  // A one-finger drag is a left drag, so it moves the tile one cell right. Cell
  // centers come from the same hook the mouse specs use.
  const fromPt = await gw.cellCenter(f.id, cx, cy);
  const toPt = await gw.cellCenter(f.id, cx + 1, cy);
  // The move's echo lands after the server oracle answers, as it did twice
  // on the loaded CI runner.
  await holdEchoFor(window, 2_000);
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

  // flake, 2026-09-20: the tap acts on the client's cache, which holds the
  // move from the release (cache.Place), while the echo is still held
  // (docs/flake-ledger.md).
  //
  // Tap descends into it; two-finger tap ascends back out.
  expect(
    await window.evaluate(
      ([id, x, y]) => (window as any).__gridwellTest.tileAt(id, x, y),
      [f.id, cx + 1, cy] as [string, number, number],
    ),
    'the client holds the move before its echo',
  ).toBe(created.id);
  await window.touchscreen.tap(toPt.x, toPt.y);
  await gw.waitIdle();
  let p = await gw.focused();
  expect(p.textFocus, 'tap descended into the text tile').not.toBe('');
  await twoFingerTap(window, { x: p.x + p.w / 2, y: p.y + p.h / 2 });
  await gw.waitIdle();
  p = await gw.focused();
  expect(p.textFocus, 'two-finger tap ascended').toBe('');
});

test('touch: a long-press lifted in place toggles the tile in its grid\'s selection', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(t, 'markdown tile created').toBeTruthy();
  await gw.waitClientTileAt(f.id, cx, cy, t.id);
  const pt = await gw.cellCenter(f.id, cx, cy);
  const selection = async () => (await gw.getGrid(f.gridID)).grid?.selection ?? [];

  await longPress(window, pt);
  await gw.waitIdle();
  await expect.poll(selection, { message: 'the hold selected the tile' }).toEqual([t.id]);
  const p = await gw.focused();
  expect(p.textFocus, 'the hold did not descend').toBe('');
  expect(p.placeDepth, 'the hold did not descend').toBe(f.placeDepth);
  const tiles = (await gw.getGrid(f.gridID)).tiles ?? [];
  expect(tiles.filter((n) => n.kind === 'text').length, 'the hold made no copy').toBe(1);

  await longPress(window, pt);
  await gw.waitIdle();
  await expect.poll(selection, { message: 'a second hold toggled it out' }).toEqual([]);
});

// A long-press is the right button and nothing more, wherever it lands: on
// the circle it opens the circle's menu, and no left press follows to open the
// + menu over it.
test('touch: a long-press on the circle opens its menu and nothing else', async ({ gw, window }) => {
  await gw.enterSource('home');
  await longPress(window, await gw.circleCenter());
  const popover = window.locator('#gw-circle-menu');
  await popover.waitFor({ timeout: 5_000 });
  await gw.waitIdle();
  await expect(popover.locator('[data-gw-choice="dump"]'), 'the menu still stands').toBeVisible();
  expect((await gw.menu()).open, 'the hold opened no + menu').toBe(false);
});

// On a crumb in the bar the same hold toggles the tile the crumb stands for,
// through the verdict a shift-click there reaches, and the pane stays put.
test('touch: a long-press lifted in place on a door crumb toggles the door in its grid\'s selection', async ({
  gw,
  window,
}) => {
  await gw.enterSource('home');
  const f0 = await gw.focused();
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const door = tileAt(await gw.getGrid(f0.gridID), 'door', cx, cy)!;
  expect(door, 'door created').toBeTruthy();
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).not.toBe(f0.gridID);
  const f1 = await gw.focused();
  const b = await gw.bar();
  const seg = b.segments.find((s) => s.tileID === door.id)!;
  expect(seg, 'the bar has the door crumb').toBeTruthy();
  const pt = { x: seg.x + seg.w / 2, y: b.top + b.height / 2 };
  const selection = async () => (await gw.getGrid(f0.gridID)).grid?.selection ?? [];

  await longPress(window, pt);
  await gw.waitIdle();
  await expect.poll(selection, { message: 'the hold selected the door' }).toEqual([door.id]);
  const p = await gw.focused();
  expect(p.gridID, 'the hold did not ascend').toBe(f1.gridID);
  expect(p.placeDepth, 'the hold did not ascend').toBe(f1.placeDepth);

  await longPress(window, pt);
  await gw.waitIdle();
  await expect.poll(selection, { message: 'a second hold toggled it out' }).toEqual([]);
});
