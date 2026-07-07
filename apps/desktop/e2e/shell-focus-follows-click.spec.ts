import { test, expect } from './fixtures';

// Issue #78: the shell ascend circle "intermittently" disappears. Root cause
// (found empirically — the reported z-order race did not reproduce): the
// xterm overlay swallows left mousedowns, and its capture listener forwarded
// only the RIGHT button — so left-clicking into a terminal from another pane
// never transferred Gridwell pane focus. Keystrokes still reached the PTY
// (DOM focus follows the click independently), so the user typed in a shell
// whose pane Gridwell considered unfocused — and the focus-gated ascend
// circle stayed hidden over exactly the shell they were using. Same bug class
// as the live-URL view's missing VIEW_LEFTDOWN forward (live-view-focus.spec).

test('left-click into a live shell transfers pane focus and shows the ascend circle', async ({
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');

  // Two panes: left keeps the grid, focus moves to the new right pane.
  await gw.splitFocusedPaneVertical();
  const panes0 = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const left = panes0[0];
  const right = panes0[panes0.length - 1];

  // A live shell in the LEFT pane.
  await window.mouse.click(left.x + left.w / 2, left.y + left.h / 2);
  await gw.waitIdle();
  const lf = await gw.focused();
  const cx = Math.round(lf.cx);
  const cy = Math.round(lf.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPaneId = (await gw.focused()).id;

  // Move Gridwell focus to the RIGHT pane via the canvas.
  await window.mouse.click(right.x + right.w / 2, right.y + right.h / 2);
  await gw.waitIdle();
  expect((await gw.focused()).id, 'focus moved off the shell pane').toBe(right.id);

  // The circle hides on the unfocused shell pane (the focused-pane-only rule).
  const circleDisplay = () =>
    window.evaluate(() => {
      const el = document.querySelector('.gw-shell-ascend') as HTMLElement | null;
      return el ? getComputedStyle(el).display : 'none'; // absent = hidden
    });
  await expect.poll(circleDisplay).toBe('none');

  // LEFT-CLICK back into the terminal: Gridwell focus must follow the click
  // (the overlay swallows the mousedown, so the overlay itself must transfer
  // it), and the ascend circle must reappear.
  await window.mouse.click(left.x + left.w / 2, left.y + left.h / 2);
  await expect
    .poll(async () => (await gw.focused()).id, { timeout: 5_000 })
    .toBe(shellPaneId);
  await expect.poll(circleDisplay).toBe('flex');

  // Leave clean: ascend via the corner circle and delete the shell tile so
  // its tmux session dies before teardown.
  await gw.rightClickPlus();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
