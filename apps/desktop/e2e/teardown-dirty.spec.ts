import { test, expect } from './fixtures';

// The harness seam: fixture teardown must complete no matter what state a
// spec ends in. A spec that dies mid-body leaves its live tiles attached —
// and before 2026-08-07 a live SHELL wedged electronApp.close() forever
// (the app process exits cleanly; the playwright-side promise never
// settles), so the worker was SIGKILLed at the test timeout, tmux servers
// and the temp home leaked, and the report gained an unattributed teardown
// error that masqueraded as a "load flake" (the shell-link-open
// misdiagnosis). These specs end DELIBERATELY dirty; the assertion is the
// teardown itself: each test passes only if the fixture cleans up inside
// the test timeout, and the leak checks (sidecar assert, tmux kill) run.

test('a spec ending with a live shell attached does not hang teardown', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      timeout: 15_000,
    })
    .toBe('webgl');
  // End here: live shell, descended pane, no cleanup — the crashed-spec state.
});

test('a spec ending with a live url view does not hang teardown', async ({ gw, window, electronApp }) => {
  await gw.enterPlugin('localdb');
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?teardown-dirty=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  // End here: live WebContentsView, no cleanup.
});
