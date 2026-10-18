import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A shell descent reconnects the running tmux and a url descent
// reopens the page, with no refresh click. A sibling pane's frozen preview is
// untouched by this pane's live descent, because going live presents the
// target without editing it: the version moves only at the ascent freeze.

test('shell descent reconnects the running tmux with its state', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // The drop lands bare, like every primitive; the first descent creates the
  // tmux, through shellconn.DecideAutoLive's fresh-shell arm.
  await gw.openMenu();
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

  // The ascent freezes and detaches; the tmux keeps running.
  await window.keyboard.type('marker=auto-live-202');
  // The marker renders only after the PTY echoes it back, so poll for that
  // round trip.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellText()), { timeout: 10_000 })
    .toContain('marker=auto-live-202');
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');

  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      message: 'the descent alone reconnects the PTY (issue #202)',
      timeout: 15_000,
    })
    .not.toBe('');
  // The typed, unentered command line is still on the PTY, so it is the same
  // tmux. The WebGL renderer paints to canvas, so the DOM carries no
  // terminal text and the buffer hook is the only way to read it.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellText()), {
      timeout: 10_000,
    })
    .toContain('marker=auto-live-202');

  // Delete the shell tile so tmux does not hang the harness close.
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

  // The drop lands bare; the first descent prompts for the address and goes
  // live, and the ascent freezes it.
  await gw.openMenu();
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

  // The row the outside sees did not change: reading and going live never
  // mutate.
  const after = tileAt(await gw.getGrid((await gw.focused()).gridID), 'url', cx, cy)!;
  expect(after.version).toBe(frozen.version);
  expect(after.previewBlobId).toBe(frozen.previewBlobId);

  await gw.middleClickCell(cx, cy); // teardown ascent
});
