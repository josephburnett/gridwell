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
// A picture draws a checked task as a stand-in, never an input; see markdown.PictureXHTML.
const checkedIn = (s: string) => (s.match(/gw-task-done/g) ?? []).length;

// Two panes on the home grid, the left descended into a text tile holding
// body, the right focused, so the left paints on canvas.
async function splitOnText(gw: any, body: string) {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) + 1;
  await gw.openMenu();
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

test('a grid preview in another pane follows unsaved typing', async ({ gw, window }) => {
  const { tile } = await splitOnText(gw, 'abc\n');
  // The right pane shows the grid the left is descended from, so the tile's
  // preview paints there on every keystroke.
  await expect.poll(async () => (await painted(window))[tile.id]).toBe('abc\n');
  await window.route(WRITE, (r: any) => r.abort());
  await window.keyboard.press('Control+End');
  await gw.typeText('x');
  await expect.poll(async () => (await painted(window))[tile.id]).toBe('abc\nx');
  await window.keyboard.press('Backspace');
  await gw.typeText('y'); // a length the preview has painted before, other bytes
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

// Where a rendered document's ink lies in the overlay's box: one band per run
// of rows holding text or a task box, with its top, height and left and right
// ends, in device px. The focused pane's overlay and the raster painted when
// focus leaves are measured in the same box on screen, so a line that moves,
// changes size or wraps elsewhere moves a band, while how either smooths a
// glyph does not.
type Band = [top: number, h: number, left: number, right: number];

async function inkBands(window: any, box: { x: number; y: number; w: number; h: number }): Promise<Band[]> {
  const png = (await window.screenshot({ clip: { x: box.x, y: box.y, width: box.w, height: box.h } })).toString('base64');
  return window.evaluate(async (png: string) => {
    const img = await createImageBitmap(await (await fetch('data:image/png;base64,' + png)).blob());
    const c = new OffscreenCanvas(img.width, img.height);
    const ctx = c.getContext('2d')!;
    ctx.drawImage(img, 0, 0);
    const d = ctx.getImageData(0, 0, img.width, img.height).data;
    const ink = (i: number) => Math.abs(d[i] - d[0]) + Math.abs(d[i + 1] - d[1]) + Math.abs(d[i + 2] - d[2]) > 90;
    const bands: Band[] = [];
    let band: Band | null = null;
    for (let y = 0; y < img.height; y++) {
      let left = -1;
      let right = -1;
      for (let x = 0; x < img.width; x++) {
        if (ink((y * img.width + x) * 4)) {
          if (left < 0) left = x;
          right = x;
        }
      }
      if (left < 0) {
        band = null;
        continue;
      }
      if (!band) bands.push((band = [y, 0, left, right]));
      band[1]++;
      band[2] = Math.min(band[2], left);
      band[3] = Math.max(band[3], right);
    }
    return bands;
  }, png);
}

// A document long enough to scroll, so the overlay keeps its scrollbar gutter,
// with three tasks, one done, one wrapping across lines.
const para =
  'The quick brown fox jumps over the lazy dog, again and again, until the paragraph wraps across ' +
  'several lines of the pane, and then some more words so that a narrower layout breaks them elsewhere.';
const todo = `# Todo\n\n- [ ] alpha\n- [x] beta\n- [ ] ${para}\n\n` + `${para}\n\n`.repeat(12);

async function sameFocusedOrNot(gw: any, window: any, zoomSteps: number) {
  const { left, right } = await splitOnText(gw, todo);
  await gw.toggleTextMode(); // rendered
  for (let i = 0; i < zoomSteps; i++) {
    await window.keyboard.press('Control+=');
    await gw.waitIdle();
  }
  const view = window.locator('#gw-rendered-view');
  await expect(view).toBeVisible();
  // The paste left the document scrolled to its end; the tasks are at the top.
  await view.evaluate((el: HTMLElement) => el.scrollTo(0, 0));
  await gw.waitIdle();
  // The overlay's box less its scrollbar, which the raster has no picture of.
  const box = await view.evaluate((el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    return { x: Math.ceil(r.left), y: Math.ceil(r.top), w: Math.floor(el.clientWidth) - 1, h: Math.floor(el.clientHeight) - 1 };
  });
  const focused = await inkBands(window, box);

  await gw.focusPane(await paneById(gw, right.id));
  await expect(view).toBeHidden();
  await expect
    .poll(async () => (await painted(window))['pane:' + left.id] ?? '', { timeout: 10_000 })
    .toMatch(/^raster:/);
  await gw.waitIdle();
  const unfocused = await inkBands(window, box);

  expect(focused.length, 'the box shows the heading, the tasks and wrapped lines').toBeGreaterThan(8);
  expect(unfocused, 'the pane moved its lines when focus left it').toEqual(focused);
}

test('a rendered pane lays its lines out the same focused or not', async ({ gw, window }) => {
  await sameFocusedOrNot(gw, window, 0);
});

test('a content-zoomed rendered pane lays its lines out the same focused or not', async ({ gw, window }) => {
  await sameFocusedOrNot(gw, window, 2);
});

test.describe('at a Retina pixel density', () => {
  test.use({ forceDeviceScaleFactor: 2 });

  test('a rendered pane lays its lines out the same focused or not', async ({ gw, window }) => {
    await sameFocusedOrNot(gw, window, 0);
  });

  test('a content-zoomed rendered pane lays its lines out the same focused or not', async ({ gw, window }) => {
    await sameFocusedOrNot(gw, window, 2);
  });
});
