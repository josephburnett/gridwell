import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import type { GridwellDriver } from './driver';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// A directory wears the outside colour wherever a swatch, a door, a crumb or
// a pane border shows it, because a directory is outside the system, and
// every one of them reads one classifier and one table (CLAUDE.md,
// 2026-11-08). The pixels are the only place they meet.

// The dark palette's DirectoryBorder and DirectoryFill, and FocusBorder, the
// grid family's line (client/theme).
const BORDER = [168, 50, 50];
const FILL = [38, 16, 15];
const BLUE = [74, 111, 255];

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub'));
fs.writeFileSync(path.join(ROOT, 'sub', 'a.md'), '# a\n');
fs.writeFileSync(path.join(ROOT, 'notes.md'), '# notes\n');
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

async function pixel(win: Page, x: number, y: number): Promise<number[]> {
  return win.evaluate(
    ([px, py]) => {
      const canvas = document.querySelector('canvas')!;
      const r = canvas.getBoundingClientRect();
      const sx = canvas.width / r.width;
      const d = canvas.getContext('2d')!.getImageData(Math.round(px * sx), Math.round(py * sx), 1, 1).data;
      return [d[0], d[1], d[2]];
    },
    [x, y],
  );
}

// The colours on a frame's left edge: a band four pixels wide from x, over
// the middle of the height from y, so a dash's gap cannot hide its colour.
async function edge(win: Page, x: number, y: number, h: number): Promise<string[]> {
  const seen = new Set<string>();
  for (let i = 0; i < 4; i++) {
    for (let j = h * 0.25; j <= h * 0.75; j += 2) seen.add(String(await pixel(win, x + i, y + j)));
  }
  return [...seen];
}

// A cell's frame: the left edge of the tile at (cx, cy).
async function cellEdge(gw: GridwellDriver, win: Page, cx: number, cy: number): Promise<string[]> {
  const f = await gw.focused();
  const c = await gw.cellCenter(f.id, cx, cy);
  const cell = (await gw.cellCenter(f.id, cx + 1, cy)).x - c.x;
  return edge(win, c.x - cell / 2, c.y - cell / 2, cell);
}

// The colours a door's interior shows, sampled on a 7x7 lattice inside its
// border: the preview of the grid it opens.
async function interior(gw: GridwellDriver, win: Page, cx: number, cy: number): Promise<string[]> {
  const f = await gw.focused();
  const c = await gw.cellCenter(f.id, cx, cy);
  const half = ((await gw.cellCenter(f.id, cx + 1, cy)).x - c.x) / 2;
  const seen = new Set<string>();
  for (let i = -3; i <= 3; i++) {
    for (let j = -3; j <= 3; j++) seen.add(String(await pixel(win, c.x + (i * half) / 4, c.y + (j * half) / 4)));
  }
  return [...seen];
}

test('a directory wears the outside colour on its swatch, its door, its crumb and its pane', async ({ gw, window }) => {
  expect(await gw.theme(), 'the colours below are the dark palette').toBe('dark');
  await gw.enterSource('home');
  await gw.waitIdle();
  const home = await gw.focused();
  expect(await pixel(window, home.x + 2, home.y + home.h / 2), 'a home grid is not outside').not.toEqual(BORDER);

  // A home door onto the root: the door is home's, its preview the directory's.
  const [cx, cy] = [Math.round(home.cx), Math.round(home.cy)];
  await gw.openMenu();
  await gw.expandSources();
  const swatch = (await gw.menu()).items.find((i) => i.isSwatch && i.label === 'docs (files)')!;
  expect(swatch, "the root's swatch is in the menu").toBeTruthy();
  expect(await edge(window, swatch.x, swatch.y, swatch.h), "the root's swatch frame is outside").toContain(String(BORDER));
  await gw.dragSourceLink('docs (files)', cx, cy);
  const door = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  expect(door?.link, 'the root is linked into home').toBe(true);
  await expect
    .poll(() => interior(gw, window, cx, cy), { message: "the door's preview wears the outside fill", timeout: 10_000 })
    .toContain(String(FILL));
  expect(await cellEdge(gw, window, cx, cy), "the door's frame is outside").toContain(String(BORDER));

  await gw.enterSource('docs (files)');
  await gw.waitIdle();
  const dir = await gw.focused();
  await expect
    .poll(() => pixel(window, dir.x + 2, dir.y + dir.h / 2), { message: "the directory's pane border is outside", timeout: 10_000 })
    .toEqual(BORDER);
  const bar = await gw.bar();
  const crumb = bar.segments.filter((sg) => sg.kind === 'chain' && !sg.closeOnly).at(-1)!;
  expect(
    await edge(window, crumb.x, bar.top, bar.height),
    "the directory's crumb frame is outside",
  ).toContain(String(BORDER));
  const sub = ((await gw.getGrid(dir.gridID)).tiles ?? []).find((t) => t.altText === 'sub')!;
  expect(sub, 'the subdirectory is listed').toBeTruthy();
  await expect
    .poll(() => interior(gw, window, Number(sub.x ?? 0), Number(sub.y ?? 0)), {
      message: "a subdirectory door's preview wears the outside fill",
      timeout: 10_000,
    })
    .toContain(String(FILL));

  // A home door onto a home grid holding a document wears none of it.
  await gw.ascendViaCrumb();
  await gw.openMenu();
  await gw.dragCreate('door', cx + 2, cy);
  await gw.descendCell(cx + 2, cy);
  const inner = await gw.focused();
  await gw.openMenu();
  await gw.dragCreate('markdown', Math.round(inner.cx), Math.round(inner.cy));
  await gw.ascendViaCrumb();
  await gw.waitIdle();
  expect(await interior(gw, window, cx + 2, cy), "a home door's preview is not outside").not.toContain(String(FILL));
  const frame = await cellEdge(gw, window, cx + 2, cy);
  expect(frame, "a home door's frame is blue").toContain(String(BLUE));
  expect(frame, "a home door's frame is not outside").not.toContain(String(BORDER));
});
