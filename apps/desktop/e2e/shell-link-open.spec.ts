import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Clicking a url rendered in a live shell opens the ephemeral visit below and
// nothing else. Every exit is instrumented: renderer window.open and confirm,
// which is xterm's OSC-8 default path, main-process shell.openExternal, and the
// BrowserWindow count. The app-wide seals keep it that way even on paths this
// spec's plain click cannot reach: openExternal is denied on every session, and
// window.open is denied on every webContents without the live-view handler.

test('a shell url click opens the visit below and nothing escapes', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
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

  // Instrument every exit before the click.
  await electronApp.evaluate(({ shell, BrowserWindow }) => {
    (global as any).__extOpens = [];
    shell.openExternal = ((u: string) => {
      (global as any).__extOpens.push(u);
      return Promise.resolve();
    }) as any;
    (global as any).__winCount = BrowserWindow.getAllWindows().length;
  });
  await window.evaluate(() => {
    (window as any).__wopens = [];
    (window as any).__realOpen = globalThis.open.bind(globalThis);
    (window as any).open = (u: any) => {
      (window as any).__wopens.push(String(u));
      return null;
    };
    (window as any).__confirms = [];
    (window as any).confirm = (m: any) => {
      (window as any).__confirms.push(String(m));
      return true;
    };
  });

  const url = `${gw.origin}/wasm_exec.js?shell-link=1`;
  await window.keyboard.type(`echo visit ${url} end`);
  await window.keyboard.press('Enter');
  // Wait for echo's output line, the exact predicate the row selection below
  // consumes. A whole-buffer `toContain` is satisfied by the typed command line,
  // which also carries the marker, so on a slow echo the selection runs before
  // the output exists and indexes the wrong row: this spec's flake was it racing
  // itself.
  const outputRow = (t: string) =>
    t.split('\n').findIndex((l) => l.includes('shell-link=1 end') && !l.includes('echo '));
  await expect
    .poll(
      async () => outputRow(await window.evaluate(() => (window as any).__gridwellTest.shellText())),
      { timeout: 10_000 },
    )
    .toBeGreaterThanOrEqual(0);

  // Click the rendered link, mapping buffer row and column to screen pixels
  // through the hook. The buffer only appends, so the row the poll found is
  // stable.
  const text: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
  const lines = text.split('\n');
  const row = outputRow(text);
  const col = lines[row].indexOf('http') + 5;
  const pt = await window.evaluate(
    ([c, r]: number[]) => (window as any).__gridwellTest.shellCellPx(c, r),
    [col, row],
  );
  // Hover, then wait for xterm's own decoration ack: it marks a hovered link by
  // putting xterm-cursor-pointer on the screen element. That beats sleeping and
  // hoping the linkifier ran. The first move is a step away, so a pointer
  // already at the target still produces a mousemove.
  await window.mouse.move(pt.x, pt.y - 40);
  await window.mouse.move(pt.x, pt.y);
  await expect
    .poll(
      () =>
        window.evaluate(
          () => document.querySelector('.xterm-screen')?.classList.contains('xterm-cursor-pointer') ?? false,
        ),
      { timeout: 5_000 },
    )
    .toBe(true);
  await window.mouse.click(pt.x, pt.y);

  // The one correct effect: a new pane below, descended into the visit.
  await expect.poll(async () => (await gw.panes()).length, { timeout: 15_000 }).toBe(2);
  await expect
    .poll(() =>
      electronApp.evaluate(({ webContents }) =>
        webContents.getAllWebContents().some((w) => w.getURL().includes('shell-link=1')),
      ),
      { timeout: 15_000 },
    )
    .toBe(true);

  // And nothing else: no renderer window.open, no xterm confirm dialog,
  // no shell.openExternal, no new BrowserWindow.
  expect(await window.evaluate(() => (window as any).__wopens)).toEqual([]);
  expect(await window.evaluate(() => (window as any).__confirms)).toEqual([]);
  expect(await electronApp.evaluate(() => (global as any).__extOpens)).toEqual([]);
  const winBefore = await electronApp.evaluate(() => (global as any).__winCount);
  expect(
    await electronApp.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().length),
  ).toBe(winBefore);

  // The app-wide seal: a bare window.open from the root renderer, which is any
  // library trying to leave, is denied. No new window, nothing external.
  await window.evaluate(() => {
    (window as any).open = (window as any).__realOpen; // the real one, for the seal probe
  });
  await window.evaluate(() => globalThis.open('https://example.com/sealed'));
  await window.waitForTimeout(500);
  expect(
    await electronApp.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().length),
  ).toBe(winBefore);
  expect(await electronApp.evaluate(() => (global as any).__extOpens)).toEqual([]);

  // Teardown: ascend the visit, ascend the shell, and delete the shell tile so
  // tmux never hangs the harness close.
  const panes = (await gw.panes()).slice().sort((a: any, b: any) => a.y - b.y);
  const lower = panes[panes.length - 1];
  await window.mouse.move(lower.x + lower.w / 2, lower.y + lower.h / 2);
  await window.mouse.down({ button: 'middle' });
  await window.mouse.up({ button: 'middle' });
  await gw.waitIdle();
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');
  const shell = tileAt(await gw.getGrid(grid), 'shell', cx, cy);
  if (shell) await gw.deleteTileCell(cx, cy);
});
