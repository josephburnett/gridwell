import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt, writeContent } from './oracle';
import { dumpNow } from './trace';

// The xterm overlay swallows left mousedowns, so its capture listener must
// forward left as well as right. Forwarding only right leaves a click into a
// terminal transferring no pane focus while keystrokes still reach the PTY, so
// the user types in a shell Gridwell considers unfocused and every focus-gated
// affordance stays hidden. The live url view's forward is the same shape.

test('left-click into a live shell transfers pane focus', async ({
  window,
  gw,
}) => {
  await gw.enterPlugin('home');

  await gw.splitFocusedPaneVertical();
  const panes0 = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const left = panes0[0];
  const right = panes0[panes0.length - 1];

  await window.mouse.click(left.x + left.w / 2, left.y + left.h / 2);
  await gw.waitIdle();
  const lf = await gw.focused();
  const cx = Math.round(lf.cx);
  const cy = Math.round(lf.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellPaneId = (await gw.focused()).id;

  await window.mouse.click(right.x + right.w / 2, right.y + right.h / 2);
  await gw.waitIdle();
  expect((await gw.focused()).id, 'focus moved off the shell pane').toBe(right.id);

  // The overlay swallows the mousedown, so it has to transfer pane focus.
  await window.mouse.click(left.x + left.w / 2, left.y + left.h / 2);
  await expect
    .poll(async () => (await gw.focused()).id, { timeout: 5_000 })
    .toBe(shellPaneId);

  // Delete the shell tile so its tmux session dies before teardown.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
});

// A live shell in the left pane, focus on the right one.
async function shellBesideGrid(gw: any) {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(left);
  const lf = await gw.focused();
  const cx = Math.round(lf.cx);
  const cy = Math.round(lf.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  await gw.focusPane(await paneOf(gw, right.id));
  expect((await gw.focused()).id, 'focus is off the shell pane').toBe(right.id);
  return { shell: left.id as string, cx, cy };
}

const paneOf = async (gw: any, id: string) => (await gw.panes()).find((p: any) => p.id === id)!;

async function teardownShell(gw: any, shell: string, cx: number, cy: number) {
  await gw.focusPane(await paneOf(gw, shell));
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(cx, cy);
}

test('one click into an unfocused live shell takes the next keystroke', async ({ window, gw }) => {
  const { shell, cx, cy } = await shellBesideGrid(gw);
  const s = await paneOf(gw, shell);
  await window.mouse.click(s.x + s.w / 2, s.y + s.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(shell);
  await window.keyboard.type("printf '%s\\n' ONE-CLICK\n");
  await gw.waitShellLine('ONE-CLICK');
  await teardownShell(gw, shell, cx, cy);
});

test('one click on a shell parked under an open menu closes the menu and takes keys', async ({ window, gw }) => {
  const { shell, cx, cy } = await shellBesideGrid(gw);
  await gw.openPalette();
  expect((await gw.palette()).open, 'the menu is open on the other pane').toBe(true);
  const s = await paneOf(gw, shell);
  await window.mouse.click(s.x + s.w / 2, s.y + s.h / 2);
  await gw.waitIdle();
  expect((await gw.palette()).open, 'the click closed the menu').toBe(false);
  expect((await gw.focused()).id, 'and focused the shell pane').toBe(shell);
  await window.keyboard.type("printf '%s\\n' PARKED-CLICK\n");
  await gw.waitShellLine('PARKED-CLICK');
  await teardownShell(gw, shell, cx, cy);
});

// Keyboard focus is set once, by the surface that took the press. A content
// fetch and the draws it causes arrive after the press and must not move it.
test('nothing deferred moves keyboard focus off a clicked shell', async ({ window, gw, home }) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const { shell, cx, cy } = await shellBesideGrid(gw);
  // A text tile the other pane shows, so a foreign write to it is fetched.
  const o = await gw.focused();
  const tx = Math.round(o.cx);
  const ty = Math.round(o.cy) + 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', tx, ty);
  const doc = tileAt(await gw.getGrid(o.gridID), 'text', tx, ty)!;
  expect(doc, 'text tile created').toBeTruthy();
  // Wait out the drop's landing, which parks the shell.
  await expect.poll(() => window.evaluate(() => (window as any).__gridwellTest.ghost().active)).toBe(false);

  const s = await paneOf(gw, shell);
  await window.mouse.click(s.x + s.w / 2, s.y + s.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(shell);
  const active = () =>
    window.evaluate(() => {
      const el = document.activeElement;
      return el ? `${el.tagName}#${el.id}.${el.className}` : 'none';
    });
  expect(await active(), 'the terminal took the press').toContain('xterm-helper-textarea');
  const domFocus = async () => {
    const { cid, lines } = await dumpNow(window, gw.origin, token);
    return lines.filter((r) => r.cid === cid && r.kind === 'dom-focus').length;
  };
  const before = await domFocus();
  await window.evaluate(() => {
    const w = globalThis as any;
    w.__focusMoves = 0;
    w.addEventListener('focusin', () => w.__focusMoves++, true);
  });

  await writeContent(gw.origin, doc.id, Number(doc.version ?? 0), Buffer.from('a foreign write'));
  const deadline = Date.now() + 2_000;
  while (Date.now() < deadline) {
    expect(await active(), 'keyboard focus stayed in the terminal').toContain('xterm-helper-textarea');
    await window.waitForTimeout(50);
  }
  expect(await window.evaluate(() => (window as any).__focusMoves), 'no focusin after the press').toBe(0);
  expect(before, 'the press itself is recorded').toBeGreaterThan(0);
  expect(await domFocus(), 'no dom-focus trace after the press').toBe(before);
  await window.keyboard.type("printf '%s\\n' STILL-HERE\n");
  await gw.waitShellLine('STILL-HERE');
  await teardownShell(gw, shell, cx, cy);
});

// A drop's landing animation parks every live surface, and the drop has
// already committed, so a press during it ends the landing and lands.
test('a click on a shell while a drop is still landing takes the keyboard', async ({ window, gw }) => {
  const { shell, cx, cy } = await shellBesideGrid(gw);
  const o = await gw.focused();
  await window.evaluate(() => (window as any).__gridwellTest.setSnapMs(60_000));
  await gw.openPalette();
  await gw.dragCreate('markdown', Math.round(o.cx), Math.round(o.cy) + 1);
  const s = await paneOf(gw, shell);
  expect(await window.evaluate(() => (window as any).__gridwellTest.ghost().active), 'the drop is landing').toBe(true);
  await window.mouse.click(s.x + s.w / 2, s.y + s.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(shell);
  await window.keyboard.type("printf '%s\\n' MID-LANDING\n");
  await gw.waitShellLine('MID-LANDING');
  await teardownShell(gw, shell, cx, cy);
});
