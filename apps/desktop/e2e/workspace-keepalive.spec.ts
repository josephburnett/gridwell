import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #249: the ONE-SURFACE rule across levels, driven through a real
// tmux session. A shell is live in the session; entering a fresh pane
// tile CAPTURES the layout (#242) — the captured shell pane TAKES OVER
// the session (the lower pane detaches; tmux state rides along, so the
// typed marker is still on screen above). Leaving the view closes its
// panes (detach), freeing the surface — and the lower pane re-engages
// (#202's owner), same session, marker still there. One tmux session,
// one attachment, at every step.

async function shellText(window: any): Promise<string> {
  return window.evaluate(() => (window as any).__gridwellTest.shellText());
}

test('a captured shell takes over the live session; leaving hands it back', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Split FIRST (splitting a descended pane would ascend it), then a live
  // shell with distinctive state in the LEFT pane.
  await gw.splitFocusedPaneVertical();
  const panes = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(panes[0]);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      timeout: 15_000,
    })
    .toBe('webgl');
  await window.keyboard.type('marker=keepalive-249');
  // The marker renders only after the PTY echoes it back — poll for that
  // round trip instead of sleeping for it.
  await expect.poll(() => shellText(window), { timeout: 10_000 }).toContain('marker=keepalive-249');

  // A pane tile in the RIGHT pane; descending from there captures the
  // layout with the shell pane still live.
  const right = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x)[1];
  await gw.focusPane(right);
  const rf = await gw.focused();
  const px = Math.round(rf.cx) + 2; // clear of the shell tile at (cx, cy)
  const py = Math.round(rf.cy);
  await gw.openPalette();
  await gw.dragCreate('pane', px, py);
  expect(tileAt(await gw.getGrid(grid), 'pane', px, py)).toBeTruthy();
  await gw.descendCell(px, py);
  await expect
    .poll(async () => window.evaluate(() => (window as any).__gridwellTest.workspace().depth))
    .toBe(1);

  // The capture cloned the shell pane; its copy TOOK OVER the session
  // (one surface per tile): focus it and the typed-but-unentered marker
  // is on the terminal — the same tmux session, not a fresh one.
  const innerShell = (await gw.panes()).find((p: any) => p.textFocus !== '');
  expect(innerShell, 'the captured layout carries the shell descent').toBeTruthy();
  await gw.focusPane(innerShell!);
  await expect.poll(() => shellText(window), { timeout: 15_000 }).toContain('marker=keepalive-249');

  // Leave the view: its panes close (detach + freeze), the surface frees,
  // and the session-level shell pane re-engages — same session, marker
  // still on the PTY.
  await gw.leaveWorkspace();
  await expect
    .poll(async () => window.evaluate(() => (window as any).__gridwellTest.workspace().depth))
    .toBe(0);
  const outerShell = (await gw.panes()).find((p: any) => p.textFocus !== '');
  expect(outerShell, 'the session shell pane survives').toBeTruthy();
  await gw.focusPane(outerShell!);
  await expect.poll(() => shellText(window), { timeout: 15_000 }).toContain('marker=keepalive-249');

  // Teardown (the shell ledger): ascend and delete the shell tile so tmux
  // never hangs the harness close.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');
  await gw.deleteTileCell(cx, cy);
});
