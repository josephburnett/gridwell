import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import type { GridwellDriver } from './driver';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// A directory wears the outside colour wherever a grid, a door or a pane
// border shows it, because a directory is outside the system (CLAUDE.md,
// 2026-11-07). The client reads it off the grid id's shape, so the pixels
// are the only place the decision and the drawing meet.

// The dark palette's DirectoryBorder and DirectoryFill (client/theme).
const BORDER = [217, 130, 43];
const FILL = [46, 31, 16];

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

test('a directory wears the outside colour on its pane, its doors and a home door onto it', async ({ gw, window }) => {
  expect(await gw.theme(), 'the colours below are the dark palette').toBe('dark');
  await gw.enterSource('home');
  await gw.waitIdle();
  const home = await gw.focused();
  expect(await pixel(window, home.x + 2, home.y + home.h / 2), 'a home grid is not outside').not.toEqual(BORDER);

  // A home door onto the root: the door is home's, its preview the directory's.
  const [cx, cy] = [Math.round(home.cx), Math.round(home.cy)];
  await gw.openMenu();
  await gw.dragSourceLink('docs (files)', cx, cy);
  const door = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  expect(door?.link, 'the root is linked into home').toBe(true);
  await expect
    .poll(() => interior(gw, window, cx, cy), { message: "the door's preview wears the outside fill", timeout: 10_000 })
    .toContain(String(FILL));

  await gw.enterSource('docs (files)');
  await gw.waitIdle();
  const dir = await gw.focused();
  await expect
    .poll(() => pixel(window, dir.x + 2, dir.y + dir.h / 2), { message: "the directory's pane border is outside", timeout: 10_000 })
    .toEqual(BORDER);
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
});
