import type { Page } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { test, expect } from './fixtures';
import { createText, createTile, getTile, getTileContent } from './oracle';
import type { GridwellDriver, PaneInfo } from './driver';
import { makeRunDir } from './homes';

// A right-drop onto a pane descended into a text in raw mode pastes the
// dropped tile's reference where the pointer is, through the editor, so the
// save claims the version and bumps it once (Joe, 2026-11-16). A left-drop is
// refused with the reason and writes nothing, a rendered pane takes no paste,
// and a text that takes no edits refuses with its reason. A reference clicked
// out of a live tile opens the place it names below.

const ROOT = makeRunDir();
test.use({ filesRoots: [{ root: ROOT, label: 'code' }] });

const BODY = 'alpha omega\n';

const notices = async (window: Page): Promise<string[]> =>
  ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).map((n: any) => String(n.message));

// Two panes side by side on home's grid, a tile to paste in the first and the
// second descended into the text a paste lands in, raw, and left unfocused.
async function twoPanes(gw: GridwellDriver, origin: string, body: string, descend: (b: PaneInfo) => Promise<void>) {
  await gw.enterSource('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const text = await createText(origin, grid, cx, cy, body);
  const pasted = await createText(origin, grid, cx + 1, cy, 'the referenced body');
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  await descend((await gw.panes()).find((p: PaneInfo) => p.id === bId)!);
  const b = (await gw.panes()).find((p: PaneInfo) => p.id === bId)!;
  const a = (await gw.panes()).find((p: PaneInfo) => p.id !== bId)!;
  await gw.focusPane(a);
  return { grid, cx, cy, text, pasted, aId: a.id, bId, b };
}

// Drags the tile at cell (cx, cy) of pane fromID to the screen point (x, y).
async function dropAt(gw: GridwellDriver, window: Page, fromID: string, cx: number, cy: number, x: number, y: number, button: 'left' | 'right') {
  const from = await gw.cellCenter(fromID, cx, cy);
  const m = window.mouse;
  await m.move(from.x, from.y);
  await m.down({ button });
  await m.move(from.x + 8, from.y + 8);
  await m.move(x, y, { steps: 10 });
  await m.up({ button });
  await gw.waitIdle();
}

test('a tile right-dropped on a raw text pane lands its reference where the pointer is', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  const s = await twoPanes(gw, origin, BODY, async () => {
    await gw.descendCell(Math.round((await gw.focused()).cx), Math.round((await gw.focused()).cy));
    await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe(BODY);
  });
  const ref = `${origin}/${s.grid}/${s.pasted.id.split('/').pop()}`;
  const before = Number((await getTile(origin, s.text.id)).version ?? 0);
  // Below the last line, inside the tile the text fills: the caret there is
  // the body's end, where the editor's own caret, at the start after the
  // switch, would not put it.
  const at = { x: s.b.x + s.b.w / 2, y: s.b.y + s.b.h * 0.6 };

  await dropAt(gw, window, s.aId, s.cx + 1, s.cy, at.x, at.y, 'left');
  await expect.poll(() => notices(window), { message: 'the left-drop was refused with the reason' })
    .toContain('only a right-drag pastes; a left-drag moves');
  expect(await getTileContent(origin, s.text.id), 'the left-drop wrote nothing').toBe(BODY);

  await dropAt(gw, window, s.aId, s.cx + 1, s.cy, at.x, at.y, 'right');
  await expect.poll(() => getTileContent(origin, s.text.id), { message: 'the reference landed at the drop point', timeout: 10_000 })
    .toBe(BODY + ref);
  expect(Number((await getTile(origin, s.text.id)).version ?? 0), 'the paste bumped the version once').toBe(before + 1);
  const pasted = await getTile(origin, s.pasted.id);
  expect([pasted.gridId, pasted.x, pasted.y], 'the dropped tile did not move').toEqual([s.pasted.gridId, s.pasted.x, s.pasted.y]);
  expect((await gw.focused()).id, 'the text pane took focus').toBe(s.bId);

  // A rendered pane takes no paste.
  await gw.toggleTextMode();
  await gw.focusPane((await gw.panes()).find((p: PaneInfo) => p.id === s.aId)!);
  await dropAt(gw, window, s.aId, s.cx + 1, s.cy, at.x, at.y, 'right');
  await gw.waitIdle();
  expect(await getTileContent(origin, s.text.id), 'a rendered pane took nothing').toBe(BODY + ref);

  // The reference, clicked in the rendered view, opens the tile it names in
  // the pane below, not a page of the client, and makes no visit.
  const scratch = await gw.scratchGridID('home');
  const urlRows = async () => ((await gw.getGrid(scratch)).tiles ?? []).filter((t: any) => t.kind === 'url').length;
  const visits = await urlRows();
  await gw.focusPane((await gw.panes()).find((p: PaneInfo) => p.id === s.bId)!);
  await window.locator('#gw-rendered-view a', { hasText: ref }).click();
  await gw.waitIdle();
  await expect.poll(async () => (await gw.panes()).length, { message: 'a pane opened below' }).toBe(3);
  const below = (await gw.panes()).find((p: PaneInfo) => p.id !== s.aId && p.id !== s.bId)!;
  await expect.poll(async () => (await gw.panes()).find((p: PaneInfo) => p.id === below.id)?.textFocus, { message: 'the pane below shows the tile' })
    .toBe(s.pasted.id);
  await expect.poll(() => gw.textareaValue(), { message: "the tile's body" }).toBe('the referenced body');
  expect(await urlRows(), 'no visit was made').toBe(visits);
});

test('a reference clicked in a shell opens the tile it names below', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const f = await gw.focused();
  const [cx, cy] = [Math.round(f.cx), Math.round(f.cy)];
  const door = await createTile(origin, f.gridID, 'door', cx + 1, cy);
  const scratch = await gw.scratchGridID('home');
  const urlRows = async () => ((await gw.getGrid(scratch)).tiles ?? []).filter((t: any) => t.kind === 'url').length;
  const visits = await urlRows();
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  const shellPane = await gw.focused();

  await gw.shellVisitURL(`${origin}/${door.childGridId}`);
  await expect.poll(async () => (await gw.panes()).length, { message: 'a pane opened below' }).toBe(2);
  const below = (await gw.panes()).find((p: PaneInfo) => p.id !== shellPane.id)!;
  await expect.poll(async () => (await gw.panes()).find((p: PaneInfo) => p.id === below.id)?.gridID, { message: "the pane below shows the door's grid" })
    .toBe(door.childGridId);
  expect((await gw.panes()).find((p: PaneInfo) => p.id === below.id)!.textFocus, 'the pane below is on a grid').toBe('');
  expect((await gw.panes()).find((p: PaneInfo) => p.id === shellPane.id)!.textFocus, 'the shell stayed live above').not.toBe('');
  expect(await urlRows(), 'no visit was made').toBe(visits);
});

test('a text that takes no edits refuses a paste with its reason', async ({ gw, window }) => {
  test.skip(process.getuid?.() === 0, 'root writes a 0444 file');
  const origin = new URL(window.url()).origin;
  const file = path.join(ROOT, 'locked.txt');
  if (fs.existsSync(file)) fs.chmodSync(file, 0o644);
  fs.writeFileSync(file, 'hands off\n');
  fs.chmodSync(file, 0o444);
  const s = await twoPanes(gw, origin, BODY, async () => {
    await gw.enterSource('code (files)');
    const f = await gw.focused();
    const tile = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t: any) => t.altText === 'locked.txt')!;
    await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
    await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  });
  await dropAt(gw, window, s.aId, s.cx + 1, s.cy, s.b.x + s.b.w / 2, s.b.y + s.b.h / 2, 'right');
  await expect.poll(() => notices(window), { message: 'the paste was refused with the reason' })
    .toContain('locked.txt cannot be written: permission denied');
  expect(fs.readFileSync(file, 'utf8')).toBe('hands off\n');
});
