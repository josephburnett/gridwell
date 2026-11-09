import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// A left-drag of a file onto a directory door moves the file there: the files
// stamp their grids with Grid.moves, and the drop verdict reads that stamp
// (dragdrop.MoveForbidden), so the gesture reaches the files' rename as the
// MCP move tool does.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
fs.writeFileSync(path.join(ROOT, 'notes.md'), '# notes\n');
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

test('a left-drag of a file onto a directory door moves it there', async ({ gw, window }) => {
  const verbs: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/{MoveTile,CreateTile,CopyTile}', async (r: any) => {
    verbs.push(r.request().url().split('/').pop());
    await r.continue().catch(() => {});
  });

  await gw.enterSource('docs (files)');
  const here = await gw.focused();
  const tiles = (await gw.getGrid(here.gridID)).tiles ?? [];
  const sub = tiles.find((t: any) => t.altText === 'sub')!;
  const notes = tiles.find((t: any) => t.altText === 'notes.md')!;
  expect(sub && notes, 'the directory and the file are listed').toBeTruthy();
  const grid = await gw.getGrid(here.gridID);
  expect(grid.grid?.moves, 'the directory grid carries the move stamp').toBe(true);

  await gw.dragTileCell(Number(notes.x ?? 0), Number(notes.y ?? 0), Number(sub.x ?? 0), Number(sub.y ?? 0));

  expect(verbs, 'the drop was a move').toEqual(['MoveTile']);
  await expect
    .poll(() => fs.existsSync(path.join(ROOT, 'sub', 'notes.md')), { message: 'the file is in the directory on disk' })
    .toBe(true);
  expect(fs.existsSync(path.join(ROOT, 'notes.md')), 'and gone from where it was').toBe(false);
  await expect
    .poll(async () => ((await gw.getGrid(sub.childGridId!)).tiles ?? []).map((t: any) => t.altText), {
      message: 'the directory lists the file',
    })
    .toContain('notes.md');
  await expect
    .poll(async () => ((await gw.getGrid(here.gridID)).tiles ?? []).some((t: any) => t.altText === 'notes.md'), {
      message: 'the old grid no longer holds it',
    })
    .toBe(false);
});

// The Gridwell layer is one space over home and every directory, so a home
// tile left-dragged onto a directory's pane moves there under its own id and
// does not cross into the disk (CLAUDE.md, "A directory is a place for
// Gridwell tiles", 2026-11-08).
test('a home tile dropped on a directory moves there and writes nothing to disk', async ({ gw, window }) => {
  const verbs: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/{MoveTile,CreateTile,CopyTile}', async (r: any) => {
    verbs.push(r.request().url().split('/').pop());
    await r.continue().catch(() => {});
  });

  await gw.sources();
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const a0 = (await gw.panes()).find((p: any) => p.id !== bId)!;
  await gw.clickScreen(a0.x + 20, a0.y + 20);
  await gw.enterSource('home');
  const a = (await gw.panes()).find((p: any) => p.id === a0.id)!;
  const [cx, cy] = [Math.round(a.cx), Math.round(a.cy) - 1];
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const door = tileAt(await gw.getGrid(a.gridID), 'door', cx, cy)!;
  expect(door, 'a home door to carry').toBeTruthy();

  const b0 = (await gw.panes()).find((p: any) => p.id === bId)!;
  await gw.clickScreen(b0.x + 20, b0.y + 20);
  await gw.enterSource('docs (files)');
  const b = (await gw.panes()).find((p: any) => p.id === bId)!;
  verbs.length = 0;
  const before = fs.readdirSync(ROOT).sort();
  const listed = (await gw.getGrid(b.gridID)).tiles ?? [];
  const [tx, ty] = [Math.round(b.cx), Math.round(b.cy) + 1];
  expect(listed.some((t: any) => Number(t.x ?? 0) === tx && Number(t.y ?? 0) === ty), 'the drop cell is empty').toBe(false);
  await gw.leftDragAcrossPanes(a.id, cx, cy, b.id, tx, ty);

  expect(verbs, 'the drop was a move').toEqual(['MoveTile']);
  await expect
    .poll(async () => tileAt(await gw.getGrid(b.gridID), 'door', tx, ty)?.id, { message: 'the door is on the directory' })
    .toBe(door.id);
  expect(tileAt(await gw.getGrid(a.gridID), 'door', cx, cy), 'and gone from home').toBeFalsy();
  expect(fs.readdirSync(ROOT).sort(), 'nothing was written to disk').toEqual(before);
});
