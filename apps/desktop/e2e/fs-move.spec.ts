import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A left-drag of a file onto a directory door moves the file there: fs
// declares move, the node stamps its grids with Grid.moves, and the drop
// verdict reads that stamp (dragdrop.MoveForbidden), so the gesture reaches
// the plugin's Move as the MCP move tool does.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
fs.writeFileSync(path.join(ROOT, 'notes.md'), '# notes\n');
test.use({ extraPlugins: [{ kind: 'fs', name: 'files', config: { root: ROOT } }] });

test('a left-drag of a file onto a directory door moves it there', async ({ gw, window }) => {
  const verbs: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/{MoveTile,CreateTile,CopyTile}', async (r: any) => {
    verbs.push(r.request().url().split('/').pop());
    await r.continue().catch(() => {});
  });

  await gw.enterPlugin('files');
  const here = await gw.focused();
  const tiles = (await gw.getGrid(here.gridID)).tiles ?? [];
  const sub = tiles.find((t: any) => t.altText === 'sub')!;
  const notes = tiles.find((t: any) => t.altText === 'notes.md')!;
  expect(sub && notes, 'the directory and the file are listed').toBeTruthy();
  const grid = await gw.getGrid(here.gridID);
  expect(grid.grid?.moves, 'the fs grid carries the move stamp').toBe(true);

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
