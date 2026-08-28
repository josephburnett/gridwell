import { test, expect } from './fixtures';

// Issue #168: a fast left-drag of a pane divider stopped tracking mid-drag on
// real input, while right-drag survived any speed. Both paths latch the
// target split at mousedown — the one structural difference was that the
// right-button mousedown calls preventDefault and the left divider-arm path
// did not, so Chromium's default action (native selection/drag) could engage
// past the OS drag threshold and steal the pointer from the canvas. That
// steal is invisible to CDP-synthesized input (Playwright dispatches straight
// to the renderer), so this spec pins the two things it CAN see: the
// divider-arm mousedown is defaultPrevented (fails before the fix), and a
// single-jump drag tracks the full distance (the latch itself).

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

  // Observe defaultPrevented AFTER the canvas handler ran (bubble phase on
  // window fires after the canvas-level listener).
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

  // One giant jump — no intermediate steps. The latched split must follow.
  await m.move(x - 300, y, { steps: 1 });
  await m.up({ button: 'left' });
  await gw.waitIdle();
  const after = (await gw.panes()).find((p: any) => p.id === left.id);
  expect(after!.w, 'the divider must track the full jump').toBeLessThan(left.w - 200);
});

// The 2026-08-07 recurrence: the drag above crosses only bare canvas, but a
// TEXT descent floats a DOM overlay (textarea / rendered view) above the
// canvas — and a fast drag whose single mousemove jumps into that rect
// hit-targets the overlay. With canvas-scoped move/up listeners the drag
// heard neither the move nor the release: it moved 0 and stayed armed after
// letting go. The fix routes move/up through window-level capture listeners
// while a gesture is in flight, so what the pointer crosses cannot matter.
// This MUST be driven with hit-tested input (window.mouse): dispatching
// events directly on the canvas element bypasses hit-testing, which is
// exactly the dimension where this class of bug lives — and why the test
// above stayed green through it.
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
  // The release over the overlay must DISARM the resize — pre-fix it stayed
  // armed until a stray canvas click (the drag "stuck to the cursor").
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.leftResizeArmed()),
    'the release over the overlay ends the gesture',
  ).toBe(false);
});
