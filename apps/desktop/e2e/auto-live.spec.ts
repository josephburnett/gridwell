import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #202 (owner decision 2026-07-26): descending IS the engagement
// gesture. A shell descent reconnects the still-running tmux session; a url
// descent reopens the page — no refresh click. The frozen preview remains
// what a tile looks like from OUTSIDE: a sibling pane's preview is untouched
// by this pane's live descent, and reading never mutates (going live is a
// presentation of the target, not an edit — the version moves only at the
// ascent freeze, as before).

test('shell descent reconnects the running session with its state', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Drag-create a shell: descends + goes live (the create path).
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      timeout: 15_000,
    })
    .not.toBe('');

  // Type state into the PTY, then ascend (freeze + detach; tmux keeps it).
  await window.keyboard.type('marker=auto-live-202');
  await window.waitForTimeout(300);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');

  // Re-descend: the SAME session must come back live with no refresh click.
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      message: 'the descent alone reconnects the PTY (issue #202)',
      timeout: 15_000,
    })
    .not.toBe('');
  // The typed (unentered) command line is still on the PTY — same session.
  // (Read via the buffer hook: the WebGL renderer paints to canvas, so the
  // DOM carries no terminal text.)
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellText()), {
      timeout: 10_000,
    })
    .toContain('marker=auto-live-202');

  // Teardown: delete the shell tile so tmux doesn't hang the harness close.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});

test('url descent reopens the page live; a sibling preview stays frozen', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Create a live url tile — drop lands it bare (#209), the first descent
  // prompts for the address and goes live — then ascend to freeze it.
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?al=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('al=1')),
        ),
      { timeout: 15_000 },
    )
    .toBe(true);
  await gw.middleClickCell(cx, cy); // ascend: freeze + tear down
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('al=1')),
        ),
      { timeout: 15_000 },
    )
    .toBe(false);
  const frozen = tileAt(await gw.getGrid((await gw.focused()).gridID), 'url', cx, cy)!;
  expect(Number(frozen.previewBlobId ?? 0), 'the freeze persisted a preview').toBeGreaterThan(0);

  // Re-descend: the page reopens live with NO refresh click (issue #202)...
  await gw.descendCell(cx, cy);
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('al=1')),
        ),
      { message: 'the descent alone reopens the url (issue #202)', timeout: 15_000 },
    )
    .toBe(true);

  // ...and the tile row the OUTSIDE sees did not change: same version, same
  // frozen preview blob (reading/going-live never mutates).
  const after = tileAt(await gw.getGrid((await gw.focused()).gridID), 'url', cx, cy)!;
  expect(after.version).toBe(frozen.version);
  expect(after.previewBlobId).toBe(frozen.previewBlobId);

  await gw.middleClickCell(cx, cy); // teardown ascent
});
