import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// A right-drag of a plugin's door onto home links it, as a left-drag does: a
// plugin door never copies (CLAUDE.md, 2026-10-20). The verdict is the
// client's, so the commit is a CreateTile and no CopyTile is asked; a plugin's
// file still copies its bytes. The server makes the same link of a CopyTile,
// so only the verb tells the client's verdict apart.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
fs.writeFileSync(path.join(ROOT, 'notes.md'), '# notes\n');
test.use({ extraPlugins: [{ kind: 'fs', name: 'files', config: { root: ROOT } }] });

test('right-drag of a plugin door onto home links it; a plugin file still copies', async ({ gw, window }) => {
  const verbs: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/{CopyTile,CreateTile}', async (r: any) => {
    verbs.push(r.request().url().split('/').pop());
    await r.continue().catch(() => {});
  });

  await gw.plugins();
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const a0 = (await gw.panes()).find((p: any) => p.id !== bId)!;
  await gw.clickScreen(a0.x + 20, a0.y + 20);
  await gw.enterPlugin('home');
  const a = (await gw.panes()).find((p: any) => p.id === a0.id)!;
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;

  const b0 = (await gw.panes()).find((p: any) => p.id === bId)!;
  await gw.clickScreen(b0.x + 20, b0.y + 20);
  await gw.enterPlugin('files');
  const b = (await gw.panes()).find((p: any) => p.id === bId)!;
  const tiles = (await gw.getGrid(b.gridID)).tiles ?? [];
  const sub = tiles.find((t: any) => t.altText === 'sub')!;
  const notes = tiles.find((t: any) => t.altText === 'notes.md')!;
  expect(sub && notes, 'the directory and the file are listed').toBeTruthy();

  await gw.copyDragAcrossPanes(b.id, Number(sub.x ?? 0), Number(sub.y ?? 0), a.id, cx, cy);
  const link = tileAt(await gw.getGrid(a.gridID), 'door', cx, cy)!;
  expect(link, 'the right-drag landed a door in home').toBeTruthy();
  expect(link.link, 'the door is a link').toBe(true);
  expect(link.childGridId, 'the link opens the directory itself').toBe(sub.childGridId);
  expect(verbs, 'the commit was the link, not a copy').toEqual(['CreateTile']);

  await gw.copyDragAcrossPanes(b.id, Number(notes.x ?? 0), Number(notes.y ?? 0), a.id, cx - 1, cy);
  const copy = tileAt(await gw.getGrid(a.gridID), 'text', cx - 1, cy)!;
  expect(copy, 'the right-drag of a file landed a text in home').toBeTruthy();
  expect(copy.link ?? false, 'the file copied, not linked').toBe(false);
  expect(await gw.getTileContent(copy.id), 'the copy holds the bytes').toBe('# notes\n');
  expect(verbs, 'the file went by CopyTile').toEqual(['CreateTile', 'CopyTile']);
});
