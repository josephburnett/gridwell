import fs from 'node:fs';
import path from 'node:path';
import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Clicking a url rendered in a live shell opens the ephemeral visit below and
// nothing else. Every exit is instrumented: renderer window.open and confirm,
// main-process shell.openExternal, and the BrowserWindow count. The app-wide
// seals cover the paths this spec's plain click cannot reach: openExternal is
// denied on every session, and window.open is denied on every webContents
// without the live-view handler.

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
  // Match echo's output row explicitly. A whole-buffer `toContain` is satisfied
  // by the typed command line, which also carries the marker, so on a slow echo
  // the selection indexes the wrong row. That was this spec's flake.
  const outputRow = (t: string) =>
    t.split('\n').findIndex((l) => l.includes('shell-link=1 end') && !l.includes('echo '));
  await expect
    .poll(
      async () => outputRow(await window.evaluate(() => (window as any).__gridwellTest.shellText())),
      { timeout: 10_000 },
    )
    .toBeGreaterThanOrEqual(0);

  // The buffer only appends, so the row the poll found is still valid.
  const text: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
  const lines = text.split('\n');
  const row = outputRow(text);
  const col = lines[row].indexOf('http') + 5;
  const pt = await window.evaluate(
    ([c, r]: number[]) => (window as any).__gridwellTest.shellCellPx(c, r),
    [col, row],
  );
  // xterm marks a hovered link with xterm-cursor-pointer on the screen element,
  // so waiting for that class replaces sleeping until the linkifier has run. The
  // first move is a step away, so a pointer already at the target still produces
  // a mousemove.
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

  // The one effect is a new pane below, descended into the visit.
  await expect.poll(async () => (await gw.panes()).length, { timeout: 15_000 }).toBe(2);
  await expect
    .poll(() =>
      electronApp.evaluate(({ webContents }) =>
        webContents.getAllWebContents().some((w) => w.getURL().includes('shell-link=1')),
      ),
      { timeout: 15_000 },
    )
    .toBe(true);

  // Nothing else: no renderer window.open, no xterm confirm dialog, no
  // shell.openExternal, no new BrowserWindow.
  expect(await window.evaluate(() => (window as any).__wopens)).toEqual([]);
  expect(await window.evaluate(() => (window as any).__confirms)).toEqual([]);
  expect(await electronApp.evaluate(() => (global as any).__extOpens)).toEqual([]);
  const winBefore = await electronApp.evaluate(() => (global as any).__winCount);
  expect(
    await electronApp.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().length),
  ).toBe(winBefore);

  // The app-wide seal denies a bare window.open from the root renderer, which
  // is any library trying to leave.
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

// The same click must not also reach the terminal application. With mouse
// reporting on, as in any TUI that tracks the mouse, xterm would both activate
// the hovered link and send the press to the PTY, so an application with
// clickable links would run its own opener on the same url and open it a second
// time in the host browser. The application here is `dd` writing every byte the
// PTY delivers to a file, so the assertion crosses click, xterm, the /shell
// WebSocket, tmux and the PTY. A click on plain text first proves reporting is
// on, so "no bytes" cannot pass because the terminal is deaf.
test('a shell url click is not also delivered to the terminal application', async ({
  gw,
  home,
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

  // The sink file holds what the application sees, byte for byte. Raw mode, so
  // the line discipline delivers the report instead of holding it for a newline,
  // and `dd bs=1`, which writes each byte through where `cat` would buffer.
  const sink = path.join(home, 'mouse-report.txt');
  const url = `${gw.origin}/wasm_exec.js?shell-click=1`;
  await window.keyboard.type(
    `printf 'visit ${url} end\\n'; printf '\\033[?1000h\\033[?1006h'; stty raw -echo; dd bs=1 of=${sink} 2>/dev/null`,
  );
  await window.keyboard.press('Enter');
  const outputRow = (t: string) =>
    t.split('\n').findIndex((l) => l.includes('shell-click=1 end') && !l.includes('printf '));
  await expect
    .poll(
      async () => outputRow(await window.evaluate(() => (window as any).__gridwellTest.shellText())),
      { timeout: 15_000 },
    )
    .toBeGreaterThanOrEqual(0);
  const text: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
  const lines = text.split('\n');
  const row = outputRow(text);
  const cellPx = (col: number) =>
    window.evaluate(
      ([c, r]: number[]) => (window as any).__gridwellTest.shellCellPx(c, r),
      [col, row],
    );
  const sunk = () => (fs.existsSync(sink) ? fs.readFileSync(sink, 'utf8') : '');

  // A press on plain text belongs to the application and arrives as an SGR
  // report. Without this the "no bytes" assertion below would pass on a terminal
  // that never reports at all.
  const plain = await cellPx(lines[row].indexOf('visit') + 2);
  await window.mouse.click(plain.x, plain.y);
  await expect.poll(sunk, { timeout: 10_000 }).toContain('\u001b[<');
  const beforeLink = sunk();

  // Hover until xterm acknowledges the decoration, then click.
  const pt = await cellPx(lines[row].indexOf('http') + 5);
  await window.mouse.move(pt.x, pt.y - 40);
  await window.mouse.move(pt.x, pt.y);
  await expect
    .poll(
      () =>
        window.evaluate(
          () =>
            document.querySelector('.xterm-screen')?.classList.contains('xterm-cursor-pointer') ??
            false,
        ),
      { timeout: 5_000 },
    )
    .toBe(true);
  await window.mouse.click(pt.x, pt.y);

  // Gridwell's one effect is the visit below.
  await expect.poll(async () => (await gw.panes()).length, { timeout: 15_000 }).toBe(2);
  // The application heard nothing: no press, no release, no bytes at all.
  await window.waitForTimeout(1000);
  expect(sunk()).toBe(beforeLink);

  // Teardown: ascend the visit, ascend the shell, delete the tile so tmux dies.
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

// A program's own OSC 8 hyperlink opens the visit below, like every other link.
// xterm's default handler for these calls confirm() and then window.open, which
// is a host-browser tab on a browser host and a denied popup on the desktop, so
// the terminal is handed Gridwell's owner instead. The sequence is fed straight
// into the terminal with shellFeed, because a hyperlink is a terminal-level
// contract and tmux strips it unless the outer terminal declares the
// capability.
test('an OSC 8 hyperlink in a shell opens the visit below, not a browser', async ({
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

  await window.evaluate(() => {
    (window as any).__wopens = [];
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

  // The renderer attaching says a terminal exists, not that its PTY does: the
  // conn is registered before the /shell socket is dialled. tmux clears the
  // screen when it paints the attach, so a row written before that is erased
  // and never returns — this test's flake. The neighbours above are immune
  // because their round trip through the PTY is itself the proof.
  await gw.shellAttached();

  // The url rides the sequence and the cells say only OSC8CLICKME, so the url
  // scanner cannot see this link. The linkifier's own hyperlink is the only one
  // here.
  const url = `${gw.origin}/wasm_exec.js?osc8=1`;
  const fed = await window.evaluate(
    (u: string) =>
      (window as any).__gridwellTest.shellFeed(
        `\r\n\u001b]8;;${u}\u001b\\OSC8CLICKME\u001b]8;;\u001b\\\r\n`,
      ),
    url,
  );
  // A feed with no live terminal writes nowhere and says so only in its return
  // value; unchecked, that reads downstream as "the row never rendered".
  expect(fed, 'shellFeed found no live terminal').toBe(true);

  // The fed row must render before it can be clicked. This poll was the flake
  // the shellAttached wait above closes; it carries the whole terminal state so
  // a bare -1 can never again mean only "not found". docs/flake-ledger.md.
  const markerRow = (t: string) => t.split('\n').findIndex((l) => l.includes('OSC8CLICKME'));
  const fedState = async () => {
    const s = await window.evaluate(() => ({
      text: (window as any).__gridwellTest.shellText() as string,
      buf: (window as any).__gridwellTest.shellBuffer(),
    }));
    const tail = s.text.split('\n').filter((l: string) => l.trim() !== '').slice(-8);
    return `row=${markerRow(s.text)} buf=${JSON.stringify(s.buf)} tail=${JSON.stringify(tail)}`;
  };
  await expect.poll(fedState, { timeout: 10_000 }).toMatch(/^row=\d/);
  const text: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
  const row = markerRow(text);
  const pt = await window.evaluate(
    ([c, r]: number[]) => (window as any).__gridwellTest.shellCellPx(c, r),
    [text.split('\n')[row].indexOf('OSC8CLICKME') + 5, row],
  );
  await window.mouse.move(pt.x, pt.y - 40);
  await window.mouse.move(pt.x, pt.y);
  await expect
    .poll(
      () =>
        window.evaluate(
          () =>
            document.querySelector('.xterm-screen')?.classList.contains('xterm-cursor-pointer') ??
            false,
        ),
      { timeout: 5_000 },
    )
    .toBe(true);
  await window.mouse.click(pt.x, pt.y);

  // The visit opens below and no browser does: no confirm, no window.open.
  await expect.poll(async () => (await gw.panes()).length, { timeout: 15_000 }).toBe(2);
  expect(await window.evaluate(() => (window as any).__confirms)).toEqual([]);
  expect(await window.evaluate(() => (window as any).__wopens)).toEqual([]);

  // Teardown: ascend the visit, ascend the shell, delete the tile so tmux dies.
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
