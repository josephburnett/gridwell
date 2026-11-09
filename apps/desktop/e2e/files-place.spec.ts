import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import type { GridwellDriver, PaneInfo } from './driver';
import { makeRunDir } from './homes';
import { getTile, tileAt } from './oracle';

// A directory is a place for Gridwell tiles (CLAUDE.md, 2026-11-08): the
// Gridwell layer is one space over home and every directory, so a Gridwell
// tile is made, moved and copied onto a directory's grid as onto a home grid,
// wearing its kind's tint among the red files, while a file onto a Gridwell
// grid links and only the file and directory primitives write the disk.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
fs.writeFileSync(path.join(ROOT, 'notes.md'), '# notes\n');
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

// The dark palette's DirectoryBorder (client/theme).
const RED = '168,50,50';

async function edgeColours(gw: GridwellDriver, win: Page, paneID: string, cx: number, cy: number): Promise<string[]> {
  const c = await gw.cellCenter(paneID, cx, cy);
  const cell = (await gw.cellCenter(paneID, cx + 1, cy)).x - c.x;
  return win.evaluate(
    ([x, y, h]) => {
      const canvas = document.querySelector('canvas')!;
      const r = canvas.getBoundingClientRect();
      const s = canvas.width / r.width;
      const ctx = canvas.getContext('2d')!;
      const seen = new Set<string>();
      for (let i = 0; i < 4; i++) {
        for (let j = h * 0.25; j <= h * 0.75; j += 2) {
          const d = ctx.getImageData(Math.round((x + i) * s), Math.round((y + j) * s), 1, 1).data;
          seen.add(`${d[0]},${d[1]},${d[2]}`);
        }
      }
      return [...seen];
    },
    [c.x - cell / 2, c.y - cell / 2, cell] as [number, number, number],
  );
}

// Two panes side by side: home on the left, the root directory on the right.
async function homeAndDirectory(gw: GridwellDriver): Promise<{ home: PaneInfo; dir: PaneInfo }> {
  await gw.sources();
  await gw.splitFocusedPaneVertical();
  const [l0, r0] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(l0);
  await gw.enterSource('home');
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === r0.id)!);
  await gw.enterSource('docs (files)');
  const panes = await gw.panes();
  return { home: panes.find((p: any) => p.id === l0.id)!, dir: panes.find((p: any) => p.id === r0.id)! };
}

// A free cell near the pane's center on its grid.
async function freeCell(gw: GridwellDriver, p: PaneInfo, dx = 0): Promise<[number, number]> {
  const tiles = (await gw.getGrid(p.gridID)).tiles ?? [];
  let [x, y] = [Math.round(p.cx) + dx, Math.round(p.cy)];
  while (tiles.some((t: any) => Number(t.x ?? 0) === x && Number(t.y ?? 0) === y)) y++;
  return [x, y];
}

async function nameModal(win: Page, name: string): Promise<void> {
  await win.waitForSelector('#gw-url-modal.open');
  await expect(win.locator('#gw-url-label')).toHaveText('name');
  await win.fill('#gw-url-input', name);
  await win.click('#gw-url-submit');
}

test('a Gridwell tile is made, moved and copied onto a directory; a file onto home links', async ({ gw, window }) => {
  const { home, dir } = await homeAndDirectory(gw);
  const before = fs.readdirSync(ROOT).sort();

  // A text from the + menu on the directory is a Gridwell row, green.
  await gw.focusPane(dir);
  const [dx, dy] = await freeCell(gw, dir);
  await gw.openMenu();
  await gw.dragCreate('markdown', dx, dy);
  const made = tileAt(await gw.getGrid(dir.gridID), 'text', dx, dy)!;
  expect(made, 'the text sits on the directory').toBeTruthy();
  expect(made.id, 'as a Gridwell row').toMatch(/\/\d+$/);
  expect(await edgeColours(gw, window, dir.id, dx, dy), 'wearing its own tint, not the red').not.toContain(RED);
  const notes = ((await gw.getGrid(dir.gridID)).tiles ?? []).find((t: any) => t.altText === 'notes.md')!;
  expect(await edgeColours(gw, window, dir.id, Number(notes.x ?? 0), Number(notes.y ?? 0)), 'among red files').toContain(RED);

  // A home text left-dragged onto the directory moves there, its id kept.
  await gw.focusPane(home);
  const [hx, hy] = await freeCell(gw, home);
  await gw.openMenu();
  await gw.dragCreate('markdown', hx, hy);
  const homeText = tileAt(await gw.getGrid(home.gridID), 'text', hx, hy)!;
  const [mx, my] = await freeCell(gw, dir, 1);
  await gw.leftDragAcrossPanes(home.id, hx, hy, dir.id, mx, my);
  await expect
    .poll(async () => tileAt(await gw.getGrid(dir.gridID), 'text', mx, my)?.id, { message: 'it moved onto the directory' })
    .toBe(homeText.id);
  expect(tileAt(await gw.getGrid(home.gridID), 'text', hx, hy), 'and left home').toBeFalsy();

  // A right-drag copies it back to home, a new Gridwell row.
  await gw.copyDragAcrossPanes(dir.id, mx, my, home.id, hx, hy);
  await expect
    .poll(async () => tileAt(await gw.getGrid(home.gridID), 'text', hx, hy)?.id, { message: 'a copy landed home' })
    .toMatch(/\/\d+$/);
  expect(tileAt(await gw.getGrid(home.gridID), 'text', hx, hy)!.id, 'its own tile').not.toBe(homeText.id);
  expect(tileAt(await gw.getGrid(dir.gridID), 'text', mx, my)?.id, 'the source stays').toBe(homeText.id);

  // A file left-dragged to home lands as a link to it.
  const [lx, ly] = await freeCell(gw, home, 1);
  await gw.leftDragAcrossPanes(dir.id, Number(notes.x ?? 0), Number(notes.y ?? 0), home.id, lx, ly);
  await expect
    .poll(async () => tileAt(await gw.getGrid(home.gridID), 'text', lx, ly)?.linkTargetId, { message: 'a link to the file' })
    .toBe(notes.id);
  expect(fs.readdirSync(ROOT).sort(), 'nothing was written to disk').toEqual(before);
});

test('the file and directory primitives make names on disk', async ({ gw, window }) => {
  await gw.enterSource('docs (files)');
  const f = await gw.focused();
  const [fx, fy] = await freeCell(gw, f);
  await gw.openMenu();
  await gw.dragCreate('file', fx, fy);
  await nameModal(window, 'made.txt');
  await expect.poll(() => fs.existsSync(path.join(ROOT, 'made.txt')), { message: 'the file is on disk' }).toBe(true);
  expect(fs.statSync(path.join(ROOT, 'made.txt')).size, 'empty').toBe(0);

  const [ex, ey] = await freeCell(gw, f, 1);
  await gw.openMenu();
  await gw.dragCreate('directory', ex, ey);
  await nameModal(window, 'made-dir');
  await expect
    .poll(() => fs.existsSync(path.join(ROOT, 'made-dir')) && fs.statSync(path.join(ROOT, 'made-dir')).isDirectory(), {
      message: 'the directory is on disk',
    })
    .toBe(true);
  await expect
    .poll(async () => ((await gw.getGrid(f.gridID)).tiles ?? []).some((t: any) => t.altText === 'made-dir' && t.kind === 'door'), {
      message: 'and its door is listed',
    })
    .toBe(true);
});

test('a shell made on a directory starts there', async ({ gw, window }) => {
  await gw.enterSource('docs (files)');
  const f = await gw.focused();
  const [sx, sy] = await freeCell(gw, f);
  await gw.openMenu();
  await gw.dragCreate('shell', sx, sy);
  await gw.descendCell(sx, sy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  await gw.shellAttached();
  await window.keyboard.type('pwd');
  await window.keyboard.press('Enter');
  const real = fs.realpathSync(ROOT);
  await expect
    .poll(
      async () =>
        ((await window.evaluate(() => (window as any).__gridwellTest.shellText())) as string)
          .split('\n')
          .some((l) => l.trim() === ROOT || l.trim() === real),
      { timeout: 15_000, message: 'pwd is the directory' },
    )
    .toBe(true);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(sx, sy);
});

// The node sends the directory to the OS trash but leaves its Gridwell rows
// on the directory's grid: the sweep to the Gridwell trash is node work not
// on main yet (CLAUDE.md, "A directory moved within Gridwell carries its
// overlay").
test.fixme('deleting a directory sends its Gridwell tiles to the Gridwell trash', async ({ gw }) => {
  fs.mkdirSync(path.join(ROOT, 'box'), { recursive: true });
  await gw.enterSource('docs (files)');
  const f = await gw.focused();
  await expect
    .poll(async () => ((await gw.getGrid(f.gridID)).tiles ?? []).some((t: any) => t.altText === 'box'))
    .toBe(true);
  const door = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t: any) => t.altText === 'box')!;
  const [bx, by] = [Number(door.x ?? 0), Number(door.y ?? 0)];
  await gw.descendCell(bx, by);
  const inside = await gw.focused();
  expect(inside.gridID, 'inside the directory').toBe(door.childGridId);
  const [tx, ty] = [Math.round(inside.cx), Math.round(inside.cy)];
  await gw.openMenu();
  await gw.dragCreate('markdown', tx, ty);
  const text = tileAt(await gw.getGrid(inside.gridID), 'text', tx, ty)!;
  expect(text, 'a Gridwell text inside it').toBeTruthy();
  await gw.ascendViaCrumb();

  await gw.deleteTileCell(bx, by);
  await expect.poll(() => fs.existsSync(path.join(ROOT, 'box')), { message: 'the directory left the disk' }).toBe(false);
  const parked = await getTile(gw.origin, text.id!);
  expect(parked.gridId, 'its Gridwell tile is in the Gridwell trash').not.toBe(door.childGridId);
  expect(parked.gridId, 'a home grid').toMatch(/\/\d+$/);
});

// Phase 2 tells a directory gone underneath Gridwell as a grey door
// (CLAUDE.md, "A directory moved within Gridwell carries its overlay"); its
// wire fact is not on main yet.
test.skip('a directory removed outside Gridwell stays a grey door holding its tiles', async () => {});
