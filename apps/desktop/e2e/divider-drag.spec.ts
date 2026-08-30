import { test, expect } from './fixtures';

// A fast left-drag of a pane divider must keep tracking. Both buttons latch the
// target split at mousedown; the difference is that the divider-arm path must
// call preventDefault, or Chromium's default action, a native selection or
// drag, engages past the OS drag threshold and steals the pointer from the
// canvas. That steal is invisible to CDP-synthesized input, which Playwright
// dispatches straight to the renderer, so this spec pins the two things it can
// see: the divider-arm mousedown is defaultPrevented, and a single-jump drag
// tracks the full distance, which is the latch itself.

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

  // Observe defaultPrevented after the canvas handler ran: the bubble phase on
  // window fires after the canvas-level listener.
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

  // One giant jump, with no intermediate steps. The latched split must follow.
  await m.move(x - 300, y, { steps: 1 });
  await m.up({ button: 'left' });
  await gw.waitIdle();
  const after = (await gw.panes()).find((p: any) => p.id === left.id);
  expect(after!.w, 'the divider must track the full jump').toBeLessThan(left.w - 200);
});

// The drag above crosses only bare canvas, but a text descent floats a DOM
// overlay, a textarea or the rendered view, above it, and a fast drag whose
// single mousemove jumps into that rect hit-targets the overlay. Canvas-scoped
// move and up listeners then hear neither the move nor the release: the drag
// moves nothing and stays armed after the button is let go. So move and up are
// routed through window-level capture listeners while a gesture is in flight,
// and what the pointer crosses cannot matter.
//
// This must be driven with hit-tested input (window.mouse). Dispatching events
// directly on the canvas element bypasses hit-testing, which is exactly the
// dimension this class of bug lives in, and why the test above cannot see it.
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
  await gw.openPalette();
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

  // Fast: press on the divider, one giant jump deep into the text pane's
  // overlay, release there.
  await window.mouse.move(gx, gy);
  await window.mouse.down();
  await window.mouse.move(gx - 300, gy, { steps: 1 });
  await window.mouse.up();
  await gw.waitIdle();

  const after = (await gw.panes()).find((p: any) => p.id === left.id)!;
  expect(left.w - after.w, 'the drag tracks across the overlay').toBeGreaterThan(200);
  // The release over the overlay must disarm the resize. Otherwise it stays
  // armed until a stray canvas click, and the drag sticks to the cursor.
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.leftResizeArmed()),
    'the release over the overlay ends the gesture',
  ).toBe(false);
});
