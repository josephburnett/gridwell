import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Descending is the engagement gesture. A shell descent reconnects the
// still-running tmux session and a url descent reopens the page, with no
// refresh click. The frozen preview stays what a tile looks like from outside:
// a sibling pane's preview is untouched by this pane's live descent. Reading
// never mutates, since going live presents the target rather than editing it;
// the version moves only at the ascent freeze.

test('shell descent reconnects the running session with its state', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Drag-create a shell: the drop lands bare, like every primitive. The first
  // descent creates the session, through DecideAutoLive's fresh-shell arm.
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await expect.poll(async () => tileAt(await gw.getGrid(f.gridID), 'shell', cx, cy)).toBeTruthy();
  await gw.waitIdle();
  expect((await gw.focused()).textFocus, 'a shell drop does not descend').toBe('');
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      timeout: 15_000,
    })
    .not.toBe('');

  // Type state into the PTY, then ascend, which freezes and detaches; tmux
  // keeps the session.
  await window.keyboard.type('marker=auto-live-202');
  // The marker renders only after the PTY echoes it back — poll for that
  // round trip instead of sleeping for it.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellText()), { timeout: 10_000 })
    .toContain('marker=auto-live-202');
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');

  // Re-descend: the same session must come back live with no refresh click.
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      message: 'the descent alone reconnects the PTY (issue #202)',
      timeout: 15_000,
    })
    .not.toBe('');
  // The typed, unentered command line is still on the PTY, so it is the same
  // session. Read through the buffer hook: the WebGL renderer paints to canvas,
  // so the DOM carries no terminal text.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellText()), {
      timeout: 10_000,
    })
    .toContain('marker=auto-live-202');

  // Teardown: delete the shell tile so tmux does not hang the harness close.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});

test('url descent reopens the page live; a sibling preview stays frozen', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Create a live url tile: the drop lands it bare, the first descent prompts
  // for the address and goes live, then the ascent freezes it.
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

  // Re-descend: the page reopens live with no refresh click...
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

  // ...and the tile row the outside sees did not change: same version, same
  // frozen preview blob. Reading and going live never mutate.
  const after = tileAt(await gw.getGrid((await gw.focused()).gridID), 'url', cx, cy)!;
  expect(after.version).toBe(frozen.version);
  expect(after.previewBlobId).toBe(frozen.previewBlobId);

  await gw.middleClickCell(cx, cy); // teardown ascent
});
