import type { ElectronApplication, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver, PaneInfo } from './driver';
import { createText, getTileContent } from './oracle';

// Typing goes to one surface, the keyboard's owner: an open input, else the
// focused pane's raw editor, live terminal or live page, else the canvas
// (CLAUDE.md, the click-lands decision). Playwright's keyboard and mouse reach
// the root page over CDP and never move OS focus, so every spec here types the
// way the OS does, into whichever webContents holds OS focus, and stages OS
// focus as a real press would leave it.

// typeOS sends each character as the OS does, to the webContents holding OS
// focus. 'Enter' is the one named key.
async function typeOS(app: ElectronApplication, keys: string[]): Promise<void> {
  await app.evaluate(async ({ webContents }, keys) => {
    for (const k of keys) {
      const wc = webContents.getFocusedWebContents();
      if (!wc) throw new Error('no webContents holds OS focus');
      wc.sendInputEvent({ type: 'keyDown', keyCode: k } as any);
      if (k.length === 1) wc.sendInputEvent({ type: 'char', keyCode: k } as any);
      wc.sendInputEvent({ type: 'keyUp', keyCode: k } as any);
    }
    await new Promise((r) => setTimeout(r, 150));
  }, keys);
}

const chars = (s: string) => [...s];

// osFocus says which webContents holds OS focus: the root's, a view's by its
// marker, or none.
function osFocus(app: ElectronApplication, marker: string): Promise<{ win: boolean; root: boolean; view: boolean }> {
  return app.evaluate(({ BrowserWindow, webContents }, marker) => {
    const win = BrowserWindow.getAllWindows()[0];
    const view = webContents.getAllWebContents().find((w) => w.getURL().includes(marker));
    return { win: win.isFocused(), root: win.webContents.isFocused(), view: view?.isFocused() ?? false };
  }, marker);
}

// pageKeys is how many keydowns the page has seen since it was armed.
function pageKeys(app: ElectronApplication, marker: string): Promise<number> {
  return app.evaluate(async ({ webContents }, marker) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(marker));
    return wc ? wc.executeJavaScript('window.__gwKeys ?? -1') : -1;
  }, marker);
}

// holdView gives the view OS focus, as a left press into it does.
async function holdView(app: ElectronApplication, marker: string): Promise<void> {
  await app.evaluate(({ webContents }, marker) => {
    webContents.getAllWebContents().find((w) => w.getURL().includes(marker))!.focus();
  }, marker);
  await expect.poll(() => osFocus(app, marker).then((f) => f.view)).toBe(true);
}

// holdRoot gives the root OS focus, as a left press on the canvas does.
async function holdRoot(app: ElectronApplication): Promise<void> {
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].webContents.focus());
}

// liveURL makes a url tile at the focused pane's center, goes live on a page
// that counts keydowns, and answers the pane and the cell.
async function liveURL(app: ElectronApplication, gw: GridwellDriver, window: Page, marker: string, dx = 0): Promise<{ paneId: string; cx: number; cy: number }> {
  const f = await gw.focused();
  const cx = Math.round(f.cx) + dx;
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?${marker}=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  await app.evaluate(async ({ webContents }, marker) => {
    const deadline = Date.now() + 15_000;
    for (;;) {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(marker));
      if (wc && !wc.isLoadingMainFrame()) {
        await wc.executeJavaScript(`window.__gwKeys = 0; window.addEventListener('keydown', () => window.__gwKeys++, true); true`);
        return;
      }
      if (Date.now() > deadline) throw new Error('the live view never loaded');
      await new Promise((r) => setTimeout(r, 50));
    }
  }, marker);
  await gw.waitIdle();
  return { paneId: (await gw.focused()).id, cx, cy };
}

test("a rename opened by a right press over a live page takes the typing, and the page gets it back after", async ({ electronApp, gw, window }) => {
  const marker = 'gwkbrename';
  await gw.enterSource('home');
  await liveURL(electronApp, gw, window, marker);
  await holdView(electronApp, marker);

  await gw.clickBarName('right');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await typeOS(electronApp, chars('abc'));
  expect(await input.inputValue(), 'the rename input holds the typing').toBe('abc');
  expect(await pageKeys(electronApp, marker), 'the page received no key').toBe(0);
  expect(await osFocus(electronApp, marker)).toEqual({ win: true, root: true, view: false });

  await typeOS(electronApp, ['Enter']);
  await expect(input).toHaveCount(0);
  await expect.poll(() => osFocus(electronApp, marker).then((f) => f.view), { message: 'the view holds the keyboard again' }).toBe(true);
  await typeOS(electronApp, chars('x'));
  expect(await pageKeys(electronApp, marker), 'a typed key reaches the page').toBe(1);
});

test('the level band rename inside a workspace tile takes the typing over a live page', async ({ electronApp, gw, window }) => {
  const marker = 'gwkbwsrename';
  await gw.enterSource('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  await gw.descendCell(wx, wy);
  await expect.poll(() => window.evaluate(() => (window as any).__gridwellTest.workspace().depth)).toBe(1);
  // Inside, the pane shows the grid the tile sits on, so the url goes beside it.
  await liveURL(electronApp, gw, window, marker, 2);
  await holdView(electronApp, marker);

  const bar = await gw.bar();
  const band = bar.segments.find((s) => s.kind === 'workspace' && s.level === 1)!;
  expect(band, 'the level band is in the bar').toBeTruthy();
  await window.mouse.click(band.x + band.w / 2, bar.top + bar.height / 2, { button: 'right' });
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await typeOS(electronApp, chars('desk'));
  expect(await input.inputValue(), 'the rename input holds the typing').toBe('desk');
  expect(await pageKeys(electronApp, marker), 'the page received no key').toBe(0);
  await typeOS(electronApp, ['Enter']);
  await expect(input).toHaveCount(0);
  await expect.poll(() => osFocus(electronApp, marker).then((o) => o.view), { message: 'the view holds the keyboard again' }).toBe(true);
});

test('a right press on another pane showing raw text gives its editor the typing', async ({ electronApp, gw, window }) => {
  const marker = 'gwkbrighttext';
  await gw.enterSource('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const text = await createText(gw.origin, grid, cx + 2, cy, 'body');
  await gw.descendCell(cx + 2, cy);
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('body');
  const textPane = (await gw.focused()).id;
  // The text pane splits; the new pane ascends and goes live on a page.
  await gw.splitFocusedPaneVertical();
  await gw.middleClickPane();
  await liveURL(electronApp, gw, window, marker);
  await holdView(electronApp, marker);

  const t = (await gw.panes()).find((p: PaneInfo) => p.id === textPane)!;
  await window.mouse.click(t.x + t.w / 2, t.y + t.h / 2, { button: 'right' });
  await gw.waitIdle();
  expect((await gw.focused()).id, 'the right press focused the text pane').toBe(textPane);
  await typeOS(electronApp, chars('zz'));
  expect(await pageKeys(electronApp, marker), 'the page received no key').toBe(0);
  await expect.poll(() => gw.textareaValue()).toContain('zz');
  await expect.poll(() => getTileContent(gw.origin, text.id), { timeout: 10_000 }).toContain('zz');
});

test('a reference opened below from a live page gives the text it opens the typing', async ({ electronApp, gw, window }) => {
  const marker = 'gwkbbelow';
  await gw.enterSource('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const text = await createText(gw.origin, grid, Math.round(f.cx) + 2, Math.round(f.cy), 'below');
  await liveURL(electronApp, gw, window, marker);
  await holdView(electronApp, marker);
  const ref = `${gw.origin}/${grid}/${text.id.split('/').pop()}`;
  await electronApp.evaluate(({ webContents }, a) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(a.marker))!;
    return wc.executeJavaScript(`window.open(${JSON.stringify(a.ref)}); true`);
  }, { marker, ref });
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe(text.id);
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('below');
  await gw.waitIdle();
  await typeOS(electronApp, chars('qq'));
  expect(await pageKeys(electronApp, marker), 'the page received no key').toBe(0);
  await expect.poll(() => gw.textareaValue()).toContain('qq');
});

test('a left press on the bar beside a live page leaves the page the keyboard', async ({ electronApp, gw, window }) => {
  const marker = 'gwkbband';
  await gw.enterSource('home');
  const { paneId } = await liveURL(electronApp, gw, window, marker);
  // An OS left press on the root makes it the first responder.
  await holdRoot(electronApp);
  // The title's left press zooms the pane and keeps it focused.
  await gw.clickBarName('left');
  await gw.waitIdle();
  expect((await gw.focused()).id).toBe(paneId);
  await expect.poll(() => osFocus(electronApp, marker).then((o) => o.view), { message: 'the view holds OS focus' }).toBe(true);
  await typeOS(electronApp, chars('k'));
  expect(await pageKeys(electronApp, marker), 'a typed key reaches the page').toBe(1);
});

test('entering a workspace tile from a live page parks its keyboard, and leaving gives it back', async ({ electronApp, gw, window }) => {
  const marker = 'gwkbwsenter';
  await gw.enterSource('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx) + 3;
  const wy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const { paneId } = await liveURL(electronApp, gw, window, marker);
  // A second pane on the grid enters the workspace; the url pane is parked
  // with the session's level.
  await gw.splitFocusedPaneVertical();
  await gw.middleClickPane();
  await holdView(electronApp, marker);
  await gw.descendCell(wx, wy);
  await expect.poll(() => window.evaluate(() => (window as any).__gridwellTest.workspace().depth)).toBe(1);
  await gw.waitIdle();
  await expect.poll(() => osFocus(electronApp, marker), { message: 'the parked view does not hold the keyboard' }).toEqual({ win: true, root: true, view: false });
  await typeOS(electronApp, chars('w'));
  expect(await pageKeys(electronApp, marker), 'the parked page received no key').toBe(0);

  await gw.leaveWorkspace(0);
  const url = (await gw.panes()).find((p: PaneInfo) => p.id === paneId)!;
  await gw.focusPane(url);
  await expect.poll(() => osFocus(electronApp, marker).then((o) => o.view), { message: 'the view holds the keyboard again' }).toBe(true);
});

test('a middle press inside a live page ascends and the root takes the keyboard', async ({ electronApp, gw, window }) => {
  const marker = 'gwkbmiddle';
  await gw.enterSource('home');
  const { paneId } = await liveURL(electronApp, gw, window, marker);
  await holdView(electronApp, marker);
  await electronApp.evaluate(({ webContents, BaseWindow }, a) => {
    const reg = (globalThis as any).__gwRegistry;
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(a.marker))!;
    const v = reg.viewBoundsFor(a.paneId);
    const cb = BaseWindow.getAllWindows()[0].getContentBounds();
    const at = { x: 60, y: 60, globalX: cb.x + v.x + 60, globalY: cb.y + v.y + 60 };
    wc.sendInputEvent({ type: 'mouseDown', ...at, button: 'middle', clickCount: 1 } as any);
    wc.sendInputEvent({ type: 'mouseUp', ...at, button: 'middle', clickCount: 1 } as any);
  }, { marker, paneId });
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].webContents.isFocused()), { message: 'the root holds the keyboard' })
    .toBe(true);
});

test('a page that reloads in a window the user left does not raise the window', async ({ electronApp, gw, window }) => {
  const marker = 'gwkbbackground';
  await gw.enterSource('home');
  await liveURL(electronApp, gw, window, marker);
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();
  await electronApp.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].blur());
  await expect.poll(() => osFocus(electronApp, marker).then((o) => o.win)).toBe(false);
  for (let i = 0; i < 2; i++) {
    await electronApp.evaluate(({ webContents }, marker) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(marker))!;
      return wc.executeJavaScript('location.reload(); true');
    }, marker);
    await window.waitForTimeout(600);
    expect((await osFocus(electronApp, marker)).win, `reload ${i}: the window stays unfocused`).toBe(false);
  }
  // A link the page opens moves pane focus, and with it the keyboard's owner,
  // and the window still stays where the user left it.
  const panes = (await gw.panes()).length;
  await electronApp.evaluate(({ webContents }, a) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes(a.marker))!;
    return wc.executeJavaScript(`window.open(${JSON.stringify(a.url)}); true`);
  }, { marker, url: `${gw.origin}/wasm_exec.js?gwkbbelowbg=1` });
  await expect.poll(async () => (await gw.panes()).length, { timeout: 10_000 }).toBe(panes + 1);
  await window.waitForTimeout(600);
  expect((await osFocus(electronApp, marker)).win, 'the opened link left the window unfocused').toBe(false);
});

