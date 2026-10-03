import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A click lands on what it hits, in any pane, and pane focus follows in the
// same press (CLAUDE.md, 2026-10-02). Every test here starts with the target
// pane unfocused and makes exactly ONE click on it. A surface that only lives
// over the focused pane (the textarea, the rendered view) or sits parked under
// an open menu gets the press from the canvas, which hands it on
// (gesture.Land).

// Two panes side by side on the same grid, the left one focused and descended
// into a text tile holding body. The tile sits one cell below center so the
// pane-center focus click on the right pane lands on no tile.
async function twoPanesWithDoc(gw: any, body: string) {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) + 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const tile = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(tile, 'text tile created').toBeTruthy();
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(left);
  await gw.descendCell(cx, cy);
  await gw.pasteText(body);
  await expect.poll(() => gw.getTileContent(tile.id), { timeout: 10_000 }).toBe(body);
  return { tile, doc: left.id as string, other: right.id as string };
}

const paneById = async (gw: any, id: string) => (await gw.panes()).find((p: any) => p.id === id)!;

test('one click on an unfocused text pane puts the caret where it landed', async ({ gw, window }) => {
  const { tile, doc, other } = await twoPanesWithDoc(gw, 'line one\nline two\nline three');

  // The painter and the textarea share one geometry, so it is measured off the
  // textarea while the doc pane holds focus.
  const g = await window.evaluate(() => {
    const ta = document.getElementById('gw-text-editor') as HTMLTextAreaElement;
    const r = ta.getBoundingClientRect();
    const cs = getComputedStyle(ta);
    const c = document.createElement('canvas').getContext('2d')!;
    c.font = `${cs.fontSize} ${cs.fontFamily}`;
    return {
      x: r.left + parseFloat(cs.paddingLeft) + parseFloat(cs.borderLeftWidth),
      y: r.top + parseFloat(cs.paddingTop) + parseFloat(cs.borderTopWidth),
      line: parseFloat(cs.lineHeight),
      adv: c.measureText('M').width,
    };
  });

  await gw.focusPane(await paneById(gw, other));
  expect((await gw.focused()).id, 'focus is on the other pane').toBe(other);

  // Row 1, between "line " and "two".
  await gw.clickScreen(g.x + 5 * g.adv + 0.2 * g.adv, g.y + 1.5 * g.line);
  expect((await paneById(gw, doc)).focused, 'the click focused the doc pane').toBe(true);
  await gw.typeText('X');
  await expect
    .poll(() => window.evaluate(() => (document.getElementById('gw-text-editor') as HTMLTextAreaElement).value))
    .toBe('line one\nline Xtwo\nline three');
  await expect.poll(() => gw.getTileContent(tile.id), { timeout: 10_000 }).toContain('line Xtwo');
});

test('one click on a checkbox in an unfocused rendered pane toggles it', async ({ gw, window }) => {
  const { tile, doc, other } = await twoPanesWithDoc(gw, '- [ ] alpha\n- [ ] beta');
  await gw.toggleTextMode(); // rendered
  const view = window.locator('#gw-rendered-view');
  await expect(view).toBeVisible();
  const box = await view.locator('input[type=checkbox]').nth(1).boundingBox();
  expect(box, 'the second checkbox is laid out').toBeTruthy();

  await gw.focusPane(await paneById(gw, other));
  await expect(view, 'the rendered view leaves with focus').toBeHidden();

  await gw.clickScreen(box!.x + box!.width / 2, box!.y + box!.height / 2);
  expect((await paneById(gw, doc)).focused, 'the click focused the doc pane').toBe(true);
  await expect.poll(() => gw.getTileContent(tile.id), { timeout: 10_000 }).toContain('- [x] beta');
  expect(await gw.getTileContent(tile.id)).toContain('- [ ] alpha');
});

test('one forwarded middle click on an unfocused live url pane focuses it and ascends', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?middle=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), { timeout: 15_000 })
    .toBeGreaterThan(wcBefore);
  const urlPaneId = (await gw.focused()).id;
  await gw.splitFocusedPaneVertical();
  expect((await gw.focused()).id).not.toBe(urlPaneId);

  const urlPane = (await gw.panes()).find((p: any) => p.id === urlPaneId)!;
  expect(urlPane.textFocus, 'the url pane is in its descent').not.toBe('');
  // A press inside the view itself: the preload forwards it, and the canvas
  // never sees it.
  await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('middle=1'))!;
    if (wc.isLoadingMainFrame()) await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
    wc.sendInputEvent({ type: 'mouseDown', x: 60, y: 60, button: 'middle', clickCount: 1 } as any);
    wc.sendInputEvent({ type: 'mouseUp', x: 60, y: 60, button: 'middle', clickCount: 1 } as any);
  });
  await expect
    .poll(async () => (await gw.panes()).find((p: any) => p.id === urlPaneId)?.textFocus, { timeout: 5_000 })
    .toBe('');
  await gw.waitIdle();
  expect((await gw.focused()).id, 'the middle click focused the url pane').toBe(urlPaneId);
});

// Playwright's keyboard writes to the root page, so the page's side of the
// keyboard is read as the OS focus its webContents holds.
test('one click on an unfocused live url pane hands its page the keyboard, and it stays', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?keys=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), { timeout: 15_000 })
    .toBeGreaterThan(wcBefore);
  const urlPaneId = (await gw.focused()).id;
  await gw.splitFocusedPaneVertical();
  expect((await gw.focused()).id).not.toBe(urlPaneId);
  const viewFocused = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().find((w) => w.getURL().includes('keys=1'))?.isFocused() ?? null,
    );
  expect(await viewFocused(), 'the page starts without the keyboard').toBe(false);

  // A press inside the view, with the widget focus Chromium applies while it
  // routes one; the canvas never sees it.
  await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('keys=1'))!;
    if (wc.isLoadingMainFrame()) await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
    wc.focus();
    wc.sendInputEvent({ type: 'mouseDown', x: 60, y: 60, button: 'left', clickCount: 1 } as any);
    wc.sendInputEvent({ type: 'mouseUp', x: 60, y: 60, button: 'left', clickCount: 1 } as any);
  });
  await expect.poll(async () => (await gw.focused()).id, { timeout: 5_000 }).toBe(urlPaneId);
  await expect.poll(viewFocused, { timeout: 5_000 }).toBe(true);
  // Past the focus guard's settle, several times over: nothing deferred takes
  // it back.
  const deadline = Date.now() + 1_500;
  while (Date.now() < deadline) {
    expect(await viewFocused(), 'the page kept the keyboard').toBe(true);
    await window.waitForTimeout(50);
  }
});
