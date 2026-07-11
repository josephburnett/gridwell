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
  await gw.enterPlugin('localdb');
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
    window.addEventListener('mousedown', (e) => {
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
