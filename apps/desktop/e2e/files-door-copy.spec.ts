import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// A right-drag of a directory's door onto home links it, as a left-drag does: a
// directory door never copies (CLAUDE.md, 2026-10-20). The verdict
// is the client's, so the commit is a CreateTile and no CopyTile is asked. The
// server makes the same link of a CopyTile, so only the verb tells the
// client's verdict apart.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

test('right-drag of a directory door onto home links it', async ({ gw, window }) => {
  const verbs: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/{CopyTile,CreateTile}', async (r: any) => {
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
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;

  const b0 = (await gw.panes()).find((p: any) => p.id === bId)!;
  await gw.clickScreen(b0.x + 20, b0.y + 20);
  await gw.enterSource('docs (files)');
  const b = (await gw.panes()).find((p: any) => p.id === bId)!;
  const tiles = (await gw.getGrid(b.gridID)).tiles ?? [];
  const sub = tiles.find((t: any) => t.altText === 'sub')!;
  expect(sub, 'the directory is listed').toBeTruthy();

  await gw.copyDragAcrossPanes(b.id, Number(sub.x ?? 0), Number(sub.y ?? 0), a.id, cx, cy);
  const link = tileAt(await gw.getGrid(a.gridID), 'door', cx, cy)!;
  expect(link, 'the right-drag landed a door in home').toBeTruthy();
  expect(link.link, 'the door is a link').toBe(true);
  expect(link.childGridId, 'the link opens the directory itself').toBe(sub.childGridId);
  expect(verbs, 'the commit was the link, not a copy').toEqual(['CreateTile']);
});
