import { test, expect } from './fixtures';

// A fast left-drag of a pane divider must keep tracking. The divider-arm path
// calls preventDefault, or Chromium's native selection or drag engages past the
// OS drag threshold and steals the pointer from the canvas. That steal is
// invisible to CDP-synthesized input, so this spec pins what it can see: the
// mousedown is defaultPrevented, and a single-jump drag tracks the full
// distance.

test('left divider-arm mousedown is defaultPrevented; single-jump drag tracks fully', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);
  const ps = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const left = ps[0];
  const x = left.x + left.w;
  const y = left.y + left.h / 2;

  // The bubble phase on window fires after the canvas listener, so what is read
  // here is the canvas handler's verdict.
  await window.evaluate(() => {
    (window as any).__gwLastMousedownPrevented = null;
    globalThis.addEventListener('mousedown', (e: MouseEvent) => {
      (window as any).__gwLastMousedownPrevented = e.defaultPrevented;
    });
  });

  const m = window.mouse;
  await m.move(x - 2, y);
  await m.down({ button: 'left' });
  expect(
    await window.evaluate(() => (window as any).__gwLastMousedownPrevented),
    'arming a divider drag must preventDefault — the unprevented native selection/drag is what steals fast drags',
  ).toBe(true);

  await m.move(x - 300, y, { steps: 1 });
  await m.up({ button: 'left' });
  await gw.waitIdle();
  const after = (await gw.panes()).find((p: any) => p.id === left.id);
  expect(after!.w, 'the divider must track the full jump').toBeLessThan(left.w - 200);
});

// A text descent floats a DOM overlay above the canvas, and a fast drag whose
// single mousemove jumps into that rect hit-targets the overlay. Canvas-scoped
// move and up listeners hear neither the move nor the release, so both are
// routed through window-level capture listeners while a gesture is in flight.
// Drive this with hit-tested input (window.mouse); events dispatched on the
// canvas element skip hit-testing and pass whatever the routing does.
test('a fast divider drag INTO a text pane keeps tracking and releases', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  let panes = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(panes[0]);
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.textareaInfo() != null), {
      message: 'the text overlay must be up before the drag crosses it',
    })
    .toBe(true);
  panes = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const left = panes[0];
  const gx = left.x + left.w;
  const gy = left.y + left.h / 2;

  await window.mouse.move(gx, gy);
  await window.mouse.down();
  await window.mouse.move(gx - 300, gy, { steps: 1 });
  await window.mouse.up();
  await gw.waitIdle();

  const after = (await gw.panes()).find((p: any) => p.id === left.id)!;
  expect(left.w - after.w, 'the drag tracks across the overlay').toBeGreaterThan(200);
  // The release over the overlay must disarm the resize, or it stays armed
  // until a stray canvas click and the drag sticks to the cursor.
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.leftResizeArmed()),
    'the release over the overlay ends the gesture',
  ).toBe(false);
});
