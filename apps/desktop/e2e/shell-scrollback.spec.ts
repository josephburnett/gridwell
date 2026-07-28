import { test, expect } from './fixtures';

// Issue #206: wheel over a live shell scrolls back through tmux history.
// The tmux client keeps xterm in the alternate buffer, where xterm turns a
// wheel into arrow keys and the 50k-line tmux history is unreachable except
// by C-b [. With `mouse on` in the generated tmux config, xterm forwards
// the wheel as mouse reports, tmux enters copy-mode on wheel-up and scrolls
// history, and wheel-down at the bottom drops back to live. This spec
// crosses the whole seam: real wheel events over the xterm overlay → mouse
// reports through the PTY relay → tmux copy-mode → redrawn screen content
// read back through the terminal buffer.

const shellText = (window: any): Promise<string> =>
  window.evaluate(() => (window as any).__gridwellTest.shellText());

test('wheel over a live shell scrolls back through tmux history (#206)', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const sx = Math.round(home.cx);
  const sy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', sx, sy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // Fill well past one screen. The -end suffix keeps "line-5-end" from
  // matching "line-50..." in the substring asserts.
  await window.keyboard.type('for i in $(seq 1 200); do echo scroll-line-$i-end; done');
  await window.keyboard.press('Enter');
  await expect.poll(() => shellText(window), { timeout: 10_000 }).toContain('scroll-line-200-end');
  expect(await shellText(window), 'early lines scrolled off the screen').not.toContain(
    'scroll-line-5-end',
  );

  // Wheel up over the terminal: tmux enters copy-mode and the earlier
  // lines come back into view.
  const p = await gw.focused();
  await window.mouse.move(p.x + p.w / 2, p.y + p.h / 2);
  for (let i = 0; i < 60; i++) {
    await window.mouse.wheel(0, -120);
  }
  await expect.poll(() => shellText(window), { timeout: 10_000 }).toContain('scroll-line-5-end');

  // Wheel back down: copy-mode exits at the bottom and the live tail is
  // visible again.
  for (let i = 0; i < 80; i++) {
    await window.mouse.wheel(0, 120);
  }
  await expect.poll(() => shellText(window), { timeout: 10_000 }).toContain('scroll-line-200-end');

  // Leave clean: ascend and delete the shell tile so its tmux session dies
  // and teardown doesn't hang on a live PTY.
  await gw.rightClickPlus();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(sx, sy);
});
