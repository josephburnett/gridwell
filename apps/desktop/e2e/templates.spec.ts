import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver, MenuItem } from './driver';
import { spawnSync } from 'node:child_process';
import { homeEnv, makeRunDir } from './homes';
import { createText, getTile, handshake, tileAt } from './oracle';

// The templates (Joe, 2026-11-09): the + menu's strip holds the copy swatch
// and the tiles of home's templates grid above the swatches, each section
// wrapped at five per row and never sharing one, three rows shown and the
// rest scrolled. The copy swatch dropped on a tile copies it in; a click on a
// template visits an ephemeral copy and leaves the template as it was; a
// template drags out as its tile does: a left-drag moves it, a right-drag
// copies it, the trashcan deletes it and a shell takes its reference.

// Five roots make seven swatches with home and its trash: two swatch rows.
const ROOTS = ['a', 'b', 'c', 'd', 'e'].map((l) => {
  const root = makeRunDir();
  fs.writeFileSync(path.join(root, 'x.txt'), 'x\n');
  return { root, label: l };
});
// An array value is spelled as a fixture tuple, or Playwright reads its
// second root as the fixture's options.
test.use({ filesRoots: [ROOTS, { option: true }] as any });

// What the node's private tmux shows of a shell, asked the way the node asks
// (shell-paste.spec.ts).
function paneText(home: string, key: string): string {
  const id = /^id:\s*"?([^"\s]+)"?/m.exec(fs.readFileSync(path.join(home, 'server.yaml'), 'utf-8'))?.[1];
  const name = 'gridwell-' + Buffer.from(key).toString('base64url');
  const r = spawnSync('tmux', ['-L', `gridwell-${id}`, 'capture-pane', '-p', '-J', '-t', name], {
    env: { ...process.env, ...homeEnv(home) },
    encoding: 'utf-8',
  });
  return r.status === 0 ? r.stdout : '';
}

async function templatesGrid(window: Page): Promise<string> {
  const id = (await handshake(new URL(window.url()).origin)).templatesGridId ?? '';
  expect(id, 'the handshake names the templates grid').not.toBe('');
  return id;
}

async function openStrip(gw: GridwellDriver): Promise<void> {
  await gw.openMenu();
  await gw.expandSources();
}

async function stripItem(gw: GridwellDriver, pred: (i: MenuItem) => boolean, what: string): Promise<MenuItem> {
  let found: MenuItem | undefined;
  await expect
    .poll(async () => {
      found = (await gw.menu()).items.find(pred);
      return !!found;
    }, { message: `the strip shows ${what}`, timeout: 10_000 })
    .toBe(true);
  return found!;
}

// A press on an item, a move past the threshold, and the release at (x, y).
async function dragItem(window: Page, item: MenuItem, x: number, y: number, button: 'left' | 'right' = 'left'): Promise<void> {
  const m = window.mouse;
  await m.move(item.x + item.w / 2, item.y + item.h / 2);
  await m.down({ button });
  await m.move(item.x + item.w / 2 + 10, item.y + item.h / 2 + 10);
  await m.move(x, y, { steps: 10 });
  await m.up({ button });
}

test('the strip: templates then swatches, five to a row, three rows shown, the rest scrolled', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  for (let i = 0; i < 5; i++) await createText(origin, tg, 0, -i, `template ${i}`);
  await gw.sources();
  await openStrip(gw);
  await expect.poll(async () => (await gw.menu()).items.filter((i) => i.template).length).toBe(5);
  await expect.poll(async () => (await gw.menu()).items.filter((i) => i.isSwatch).map((i) => i.label), { message: 'every root is a swatch' }).toHaveLength(7);

  const menu = await gw.menu();
  const strip = menu.items.filter((i) => i.strip);
  const templates = strip.filter((i) => i.copySwatch || i.template);
  const swatches = strip.filter((i) => i.isSwatch);
  expect(templates[0].copySwatch, 'the copy swatch comes first').toBe(true);
  expect(templates.length, 'the copy swatch and five templates').toBe(6);
  expect(swatches.length, 'home, its trash and five roots').toBe(7);
  // Newest first: the last made is the first template.
  expect((await getTile(origin, templates[1].template!)).altText).toBe('template 4');

  const rows = (items: MenuItem[]) => [...new Set(items.map((i) => i.y))].sort((a, b) => a - b);
  const all = rows(strip);
  for (const y of all) {
    expect(strip.filter((i) => i.y === y).length, `row ${y} holds at most five`).toBeLessThanOrEqual(5);
    const kinds = new Set(strip.filter((i) => i.y === y).map((i) => (i.isSwatch ? 'swatch' : 'template')));
    expect(kinds.size, `row ${y} holds one section`).toBe(1);
  }
  expect(all.length, 'two template rows and two swatch rows').toBe(4);
  expect(Math.max(...templates.map((i) => i.y)), 'the templates sit above the swatches').toBeLessThan(
    Math.min(...swatches.map((i) => i.y)),
  );

  // Three rows show; the fourth lies past the strip's band until scrolled.
  const band = menu.strip;
  const inBand = (i: MenuItem, b: typeof band) => i.y >= b.y - 0.5 && i.y + i.h <= b.y + b.h + 0.5;
  expect(rows(strip.filter((i) => inBand(i, band))).length, 'three rows shown').toBe(3);
  const last = swatches[swatches.length - 1];
  expect(inBand(last, band), 'the last swatch row is scrolled out').toBe(false);
  await window.mouse.move(band.x + band.w / 2, band.y + band.h / 2);
  await window.mouse.wheel(0, 400);
  await expect.poll(async () => (await gw.menu()).strip.scroll, { message: 'the wheel scrolled the strip' }).toBeGreaterThan(0);
  const scrolled = await gw.menu();
  const lastNow = scrolled.items.find((i) => i.isSwatch && i.label === last.label)!;
  expect(inBand(lastNow, scrolled.strip), 'the last swatch row is shown').toBe(true);
  expect((await gw.focused()).gridID, 'the wheel over the menu zoomed nothing behind it').toBeTruthy();
});

test('the copy swatch on a text makes a template; a click visits an ephemeral copy and leaves it as it was', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  const text = await createText(origin, home.gridID, cx, cy, 'a note to copy');
  await gw.waitClientTileAt(home.id, cx, cy, text.id);

  await openStrip(gw);
  const plus = await stripItem(gw, (i) => !!i.copySwatch, 'the copy swatch');
  const at = await gw.cellCenter(home.id, cx, cy);
  await dragItem(window, plus, at.x, at.y);
  await gw.waitIdle();
  await expect.poll(async () => ((await gw.getGrid(tg)).tiles ?? []).length, { message: 'the text was copied in' }).toBe(1);
  const tpl = ((await gw.getGrid(tg)).tiles ?? [])[0];
  expect(tpl.kind).toBe('text');
  expect(tpl.id, 'a copy, not the text').not.toBe(text.id);
  expect(tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)?.id, 'the text stayed').toBe(text.id);

  const item = await stripItem(gw, (i) => i.template === tpl.id, 'the new template');
  const before = await getTile(origin, tpl.id);
  const scratch = await gw.scratchGridID('home');
  await window.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const visit = (await gw.focused()).textFocus;
  expect(visit, 'the visit is a copy, not the template').not.toBe(tpl.id);
  expect(((await gw.getGrid(scratch)).tiles ?? []).map((t) => t.id), 'the copy is in the scratch grid').toContain(visit);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);

  await gw.ascendViaCrumb();
  await expect
    .poll(async () => ((await gw.getGrid(scratch)).tiles ?? []).some((t) => t.id === visit), { timeout: 10_000 })
    .toBe(false);
  expect(await getTile(origin, tpl.id), 'the template is byte for byte as it was, its preview included').toEqual(before);
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no notice from the round trip').toHaveLength(0);
});

test('a template drags out: left moves it, right copies it, the trashcan deletes it', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const moved = await createText(origin, tg, 0, 0, 'moved out');
  const copied = await createText(origin, tg, 0, -1, 'copied out');
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await openStrip(gw);
  let a = await stripItem(gw, (i) => i.template === moved.id, 'the template to move');
  let to = await gw.cellCenter(home.id, cx, cy);
  await dragItem(window, a, to.x, to.y);
  await gw.waitIdle();
  await expect.poll(async () => tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)?.id, { message: 'the left-drag moved it' }).toBe(moved.id);
  expect(((await gw.getGrid(tg)).tiles ?? []).some((t) => t.id === moved.id), 'it left the templates').toBe(false);

  await openStrip(gw);
  a = await stripItem(gw, (i) => i.template === copied.id, 'the template to copy');
  to = await gw.cellCenter(home.id, cx + 1, cy);
  await dragItem(window, a, to.x, to.y, 'right');
  await gw.waitIdle();
  await expect.poll(async () => tileAt(await gw.getGrid(home.gridID), 'text', cx + 1, cy)?.id ?? '', { message: 'the right-drag copied it' }).not.toBe('');
  expect(tileAt(await gw.getGrid(home.gridID), 'text', cx + 1, cy)?.id, 'a copy').not.toBe(copied.id);
  expect(((await gw.getGrid(tg)).tiles ?? []).some((t) => t.id === copied.id), 'the template stayed').toBe(true);

  await openStrip(gw);
  a = await stripItem(gw, (i) => i.template === copied.id, 'the template to delete');
  const menu = await gw.menu();
  await dragItem(window, a, menu.plusX, menu.plusY);
  await gw.waitIdle();
  await expect.poll(async () => ((await gw.getGrid(tg)).tiles ?? []).length, { message: 'the trashcan deleted it' }).toBe(0);
});

test('the copy swatch on a shell copies it in; a template dropped on a shell pastes its reference', async ({ gw, window, home: nodeDir }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy) - 1;
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  const shell = tileAt(await gw.getGrid(home.gridID), 'shell', cx, cy)!;
  expect(shell, 'the shell was made').toBeTruthy();

  await openStrip(gw);
  const plus = await stripItem(gw, (i) => !!i.copySwatch, 'the copy swatch');
  const at = await gw.cellCenter(home.id, cx, cy);
  await dragItem(window, plus, at.x, at.y);
  await gw.waitIdle();
  await expect.poll(async () => ((await gw.getGrid(tg)).tiles ?? []).map((t) => t.kind), { message: 'the shell was copied in, not pasted into' }).toEqual(['shell']);

  // A template dropped on the shell tile pastes its reference into the tmux
  // and moves nothing.
  const note = await createText(origin, tg, 3, 3, 'a reference');
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  await gw.ascendViaCrumb();
  await openStrip(gw);
  const item = await stripItem(gw, (i) => i.template === note.id, 'the template to paste');
  await dragItem(window, item, at.x, at.y);
  await gw.waitIdle();
  expect(((await gw.getGrid(tg)).tiles ?? []).some((t) => t.id === note.id), 'the paste moved nothing').toBe(true);
  const ref = `${origin}/${tg}/${note.id.split('/').pop()}`;
  const key = shell.id.split('/').pop()!;
  await expect
    .poll(() => paneText(nodeDir, key).includes(ref), { message: `the reference ${ref} was pasted`, timeout: 10_000 })
    .toBe(true);
});

test('a click on a door template enters an ephemeral copy of it; leaving retires the copy', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const res = await fetch(`${origin}/gridwell.v1.Gridwell/CreateTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', Cookie: (await window.context().cookies()).map((c) => `${c.name}=${c.value}`).join('; ') },
    body: JSON.stringify({ gridId: tg, tile: { kind: 'door', x: 0, y: 0, w: 1, h: 1, altText: 'a door' } }),
  });
  expect(res.ok, 'the door template was made').toBe(true);
  const door = ((await res.json()) as { tile: { id: string; childGridId: string } }).tile;
  await gw.enterSource('home');
  const home = await gw.focused();
  const scratch = await gw.scratchGridID('home');
  await openStrip(gw);
  const item = await stripItem(gw, (i) => i.template === door.id, 'the door template');
  await window.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
  await gw.waitIdle();
  const copy = ((await gw.getGrid(scratch)).tiles ?? []).find((t) => t.kind === 'door');
  expect(copy, 'the copy is in the scratch grid').toBeTruthy();
  await expect.poll(async () => (await gw.focused()).gridID, { message: "the pane entered the copy's grid" }).toBe(copy!.childGridId);
  expect(copy!.childGridId, 'a deep copy has its own grid').not.toBe(door.childGridId);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(home.gridID);
  await expect
    .poll(async () => ((await gw.getGrid(scratch)).tiles ?? []).some((t) => t.id === copy!.id), { message: 'leaving retired the copy', timeout: 10_000 })
    .toBe(false);
  expect(((await gw.getGrid(tg)).tiles ?? []).map((t) => t.id), 'the template stayed').toEqual([door.id]);
});
