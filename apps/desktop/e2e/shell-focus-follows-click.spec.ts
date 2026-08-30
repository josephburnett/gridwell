import { test, expect } from './fixtures';

// The xterm overlay swallows left mousedowns, so its capture listener must
// forward the left button as well as the right. Forwarding only the right leaves
// a left-click into a terminal from another pane transferring no pane focus,
// while keystrokes still reach the PTY, since DOM focus follows the click
// independently: the user types in a shell whose pane Gridwell considers
// unfocused, and every focus-gated affordance stays hidden over the shell they
// are using. The live url view's VIEW_LEFTDOWN forward is the same shape (see
// live-view-focus.spec).

test('left-click into a live shell transfers pane focus', async ({
  window,
  gw,
}) => {
  await gw.enterPlugin('home');

  // Two panes: the left keeps the grid, focus moves to the new right pane.
  await gw.splitFocusedPaneVertical();
  const panes0 = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const left = panes0[0];
  const right = panes0[panes0.length - 1];

  // A live shell in the left pane.
  await window.mouse.click(left.x + left.w / 2, left.y + left.h / 2);
  await gw.waitIdle();
  const lf = await gw.focused();
  const cx = Math.round(lf.cx);
  const cy = Math.round(lf.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPaneId = (await gw.focused()).id;

  // Move pane focus to the right pane through the canvas.
  await window.mouse.click(right.x + right.w / 2, right.y + right.h / 2);
  await gw.waitIdle();
  expect((await gw.focused()).id, 'focus moved off the shell pane').toBe(right.id);

  // Left-click back into the terminal: pane focus must follow the click. The
  // overlay swallows the mousedown, so the overlay itself has to transfer it.
  await window.mouse.click(left.x + left.w / 2, left.y + left.h / 2);
  await expect
    .poll(async () => (await gw.focused()).id, { timeout: 5_000 })
    .toBe(shellPaneId);

  // Leave clean: ascend through the bar slot and delete the shell tile so its
  // tmux session dies before teardown.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
