import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #224: when a canvas-overlay gesture parks the live shell overlay
// (opening the + menu on ANOTHER pane, dragging a divider across it), the
// canvas paints the frozen snapshot in its place. The live xterm canvas is
// an integer number of cells — a little smaller than the content box, and
// top-left anchored — but the stand-in used to contain-fit the snapshot
// into the whole box: centered and scaled up by the leftover cell
// fraction, so the terminal pixels visibly shifted on every park/unpark.
//
// The spec crosses the seam: the rect the renderer draws the stand-in at
// (the shellStandin hook reads the same shellStandinRect the draw path
// uses) must match the live xterm canvas's own screen rect.
test('the parked shell stand-in sits exactly where the live canvas was', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');

  // Two panes: the shell lives in A; the + menu will open on B.
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length).toBe(2);

  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // a drop lands bare (#241); the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPaneId = (await gw.focused()).id;
  const shell = tileAt(await gw.getGrid(a.gridID), 'shell', cx, cy)!;
  expect(shell, 'shell created').toBeTruthy();

  // Content on screen, then a freeze (ascend) so a preview snapshot exists,
  // then descend back into the live session.
  await window.keyboard.type('echo STANDIN-MARKER');
  await window.keyboard.press('Enter');
  await window.waitForTimeout(500);
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(a.gridID), 'shell', cx, cy)?.previewBlobId ?? 0), {
      timeout: 10_000,
    })
    .toBeGreaterThan(0);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  // Focus the OTHER pane (a focus-only click), with the shell overlay still
  // visible on its unfocused pane, and record the live canvas's screen rect.
  const other = (await gw.panes()).find((p) => p.id !== shellPaneId)!;
  await gw.clickScreen(other.x + other.w / 2, other.y + other.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(other.id);
  const live = await window.evaluate(() => {
    const host = document.querySelector('.gw-shell-host');
    if (!host) return null;
    const canvas = [...host.querySelectorAll('canvas')].find((c) => c.className === '');
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

  // The stand-in must sit exactly where the live canvas was — same origin,
  // same size (sub-pixel slack only). Contain-fit failed this by the
  // centering offset and the scale-up.
  expect(Math.abs(standin.x - live!.x), 'x').toBeLessThan(1.5);
  expect(Math.abs(standin.y - live!.y), 'y').toBeLessThan(1.5);
  expect(Math.abs(standin.w - live!.w), 'w').toBeLessThan(1.5);
  expect(Math.abs(standin.h - live!.h), 'h').toBeLessThan(1.5);

  // Leave clean: refocus the shell pane (the focus transfer closes the
  // menu), ascend, and delete the tile so its tmux session dies before
  // teardown.
  await gw.focusPane((await gw.panes()).find((p) => p.id === shellPaneId)!);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});
