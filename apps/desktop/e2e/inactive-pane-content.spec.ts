import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// A pane shows the same document whether or not it has focus. The focused
// pane shows the bytes themselves (the textarea, the rendered overlay); every
// other pane paints a picture of them (a wrap, a raster) keyed by
// cache.BodyGen, so that key must move with every change to the bytes, an
// unsaved edit included. Saves are held here so an edit stays unsaved: before
// the fix the picture of the bytes before the edit kept matching, and the pane
// the user just left showed the older document until a save landed.

const ROOT = makeRunDir();
test.use({ extraPlugins: [{ kind: 'fs', name: 'code', config: { root: ROOT } }] });

const WRITE = '**/gridwell.v1.Gridwell/WriteContent';
const painted = (window: any): Promise<Record<string, string>> =>
  window.evaluate(() => (window as any).__gridwellTest.paintedText());
const paneById = async (gw: any, id: string) => (await gw.panes()).find((p: any) => p.id === id)!;
const checkedIn = (s: string) => (s.match(/<input[^>]*checked/g) ?? []).length;

// Two panes on the home grid, the left descended into a text tile holding
// body, the right focused, so the left paints on canvas.
async function splitOnText(gw: any, body: string) {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) + 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const tile = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(left);
  await gw.descendCell(cx, cy);
  await gw.pasteText(body);
  await expect.poll(() => gw.getTileContent(tile.id), { timeout: 10_000 }).toBe(body);
  return { left, right, tile };
}

test('an unsaved raw edit shows in the pane focus left, same length or not', async ({ gw, window }) => {
  const { left, right } = await splitOnText(gw, 'one two\n');
  await gw.focusPane(await paneById(gw, right.id));
  await expect.poll(async () => (await painted(window))['pane:' + left.id]).toBe('one two\n');

  await gw.focusPane(await paneById(gw, left.id));
  await window.route(WRITE, (r: any) => r.abort());
  await window.keyboard.press('Control+End');
  await window.keyboard.press('Backspace');
  await gw.typeText('!'); // the same length as the body the picture was made of
  expect(await gw.textareaValue()).toBe('one two!');

  await gw.focusPane(await paneById(gw, right.id));
  await expect
    .poll(async () => (await painted(window))['pane:' + left.id], { timeout: 5_000 })
    .toBe('one two!');
  await window.unroute(WRITE);
});

test('an unsaved checkbox flip shows in the rendered pane focus left', async ({ gw, window }) => {
  const { left, right } = await splitOnText(gw, '# Todo\n\n- [ ] alpha\n- [x] beta\n');
  await gw.toggleTextMode(); // rendered
  await gw.focusPane(await paneById(gw, right.id));
  await expect
    .poll(async () => (await painted(window))['pane:' + left.id] ?? '', { timeout: 10_000 })
    .toMatch(/^raster:/);
  const before = checkedIn((await painted(window))['pane:' + left.id]);

  await gw.focusPane(await paneById(gw, left.id));
  await window.route(WRITE, (r: any) => r.abort());
  const boxes = window.locator('#gw-rendered-view input[type=checkbox]');
  await boxes.nth(0).click();
  await expect(boxes.nth(0)).toBeChecked();

  await gw.focusPane(await paneById(gw, right.id));
  await expect
    .poll(async () => checkedIn((await painted(window))['pane:' + left.id] ?? ''), { timeout: 5_000 })
    .toBe(before + 1);
  await window.unroute(WRITE);
});

test('a grid face in another pane follows unsaved typing', async ({ gw, window }) => {
  const { tile } = await splitOnText(gw, 'abc\n');
  // The right pane shows the grid the left is descended from, so the tile's
  // face paints there on every keystroke.
  await expect.poll(async () => (await painted(window))[tile.id]).toBe('abc\n');
  await window.route(WRITE, (r: any) => r.abort());
  await window.keyboard.press('Control+End');
  await gw.typeText('x');
  await expect.poll(async () => (await painted(window))[tile.id]).toBe('abc\nx');
  await window.keyboard.press('Backspace');
  await gw.typeText('y'); // a length the face has painted before, other bytes
  await expect
    .poll(async () => (await painted(window))[tile.id], { timeout: 5_000 })
    .toBe('abc\ny');
  await window.unroute(WRITE);
});

test('an fs file changed on disk shows its new bytes in the pane focus left', async ({ gw, window }) => {
  const file = path.join(ROOT, 'notes.txt');
  fs.writeFileSync(file, 'version one\n');
  await gw.enterPlugin('code');
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(left);
  const tile = ((await gw.getGrid(left.gridID)).tiles ?? []).find((t: any) => t.altText === 'notes.txt')!;
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('version one\n');

  const later = new Date(Date.now() + 2000);
  fs.writeFileSync(file, 'version two, from disk\n');
  fs.utimesSync(file, later, later);
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('version two, from disk\n');

  await gw.focusPane(await paneById(gw, right.id));
  await expect
    .poll(async () => (await painted(window))['pane:' + left.id], { timeout: 5_000 })
    .toBe('version two, from disk\n');
});

