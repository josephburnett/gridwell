import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// When a canvas-overlay gesture parks the live shell overlay, by opening the +
// menu on another pane or dragging a divider across it, the canvas paints the
// frozen snapshot in its place. The live xterm canvas is an integer number of
// cells, a little smaller than the content box and anchored top-left, so
// contain-fitting the snapshot into the whole box would center it and scale it
// up by the leftover cell fraction, visibly shifting the terminal pixels on
// every park and unpark.
//
// The spec crosses that seam: the rect the renderer draws the stand-in at, read
// through the shellStandin hook from the same shellStandinRect the draw path
// uses, must match the live xterm canvas's own screen rect.
test('the parked shell stand-in sits exactly where the live canvas was', async ({ gw, window }) => {
  await gw.enterPlugin('home');

  // Two panes: the shell lives in A, and the + menu opens on B.
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length).toBe(2);

  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPaneId = (await gw.focused()).id;
  const shell = tileAt(await gw.getGrid(a.gridID), 'shell', cx, cy)!;
  expect(shell, 'shell created').toBeTruthy();

  // Content on screen, then an ascent to freeze so a preview snapshot exists,
  // then descend back into the live session.
  await window.keyboard.type('echo STANDIN-MARKER');
  await window.keyboard.press('Enter');
  // Wait for echo's output line. The typed command also carries the marker, so a
  // whole-buffer match would pass early.
  await expect
    .poll(async () => {
      const t: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
      return t.split('\n').some((l) => l.includes('STANDIN-MARKER') && !l.includes('echo '));
    }, { timeout: 10_000 })
    .toBe(true);
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(a.gridID), 'shell', cx, cy)?.previewBlobId ?? 0), {
      timeout: 10_000,
    })
    .toBeGreaterThan(0);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // Focus the other pane with a focus-only click, leaving the shell overlay
  // visible on its now-unfocused pane, and record the live canvas's screen rect.
  const other = (await gw.panes()).find((p) => p.id !== shellPaneId)!;
  await gw.clickScreen(other.x + other.w / 2, other.y + other.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(other.id);
  const live = await window.evaluate(() => {
    const host = document.querySelector('.gw-shell-host');
    if (!host) return null;
    const canvas = Array.from(host.querySelectorAll('canvas')).find((c) => c.className === '');
    if (!canvas) return null;
    const r = canvas.getBoundingClientRect();
    return { x: r.x, y: r.y, w: r.width, h: r.height };
  });
  expect(live, 'live xterm canvas visible before the park').toBeTruthy();

  // Open the + menu on the grid pane: overlays park, the stand-in draws.
  await gw.openPalette();
  const standin = await window.evaluate(
    (id: string) => (window as any).__gridwellTest.shellStandin(id),
    shellPaneId,
  );
  expect(standin, 'stand-in rect resolvable while parked').toBeTruthy();

  // The stand-in must sit exactly where the live canvas was: same origin, same
  // size, with only sub-pixel slack. A contain-fit misses by the centering offset
  // and the scale-up.
  expect(Math.abs(standin.x - live!.x), 'x').toBeLessThan(1.5);
  expect(Math.abs(standin.y - live!.y), 'y').toBeLessThan(1.5);
  expect(Math.abs(standin.w - live!.w), 'w').toBeLessThan(1.5);
  expect(Math.abs(standin.h - live!.h), 'h').toBeLessThan(1.5);

  // Leave clean: refocus the shell pane, which closes the menu through the focus
  // transfer, ascend, and delete the tile so its tmux session dies before
  // teardown.
  await gw.focusPane((await gw.panes()).find((p) => p.id === shellPaneId)!);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
