import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';

// Markdown crosses the files boundary by copy (CLAUDE.md, 2026-11-08): a
// right-drag of a home text into a directory makes a markdown file at the
// drop's cell, a markdown file right-dragged into home is a text holding its
// bytes, and any other file lands in home as a link.

const ROOT = makeRunDir();
fs.writeFileSync(path.join(ROOT, 'notes.md'), '# notes\n\nkept on disk\n');
fs.writeFileSync(path.join(ROOT, 'plain.txt'), 'plain\n');
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

test('markdown copies into a directory and out of it; any other file links', async ({ gw }) => {
  await gw.sources();
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const a0 = (await gw.panes()).find((p: any) => p.id !== bId)!;
  await gw.clickScreen(a0.x + 20, a0.y + 20);
  await gw.enterSource('home');
  const a = (await gw.panes()).find((p: any) => p.id === a0.id)!;
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;

  // A home text with a body, made as the user makes one.
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const text = tileAt(await gw.getGrid(a.gridID), 'text', cx, cy)!;
  expect(text, 'a home text was made').toBeTruthy();
  await gw.descendCell(cx, cy);
  await gw.typeText('Plan\n\nsteps');
  await gw.ascendViaCrumb();
  await expect.poll(async () => gw.getTileContent(text.id), { timeout: 10_000 }).toBe('Plan\n\nsteps');

  const b0 = (await gw.panes()).find((p: any) => p.id === bId)!;
  await gw.clickScreen(b0.x + 20, b0.y + 20);
  await gw.enterSource('docs (files)');
  const b = (await gw.panes()).find((p: any) => p.id === bId)!;
  const listed = (await gw.getGrid(b.gridID)).tiles ?? [];
  const notes = listed.find((t: any) => t.altText === 'notes.md')!;
  const plain = listed.find((t: any) => t.altText === 'plain.txt')!;
  expect(notes && plain, 'both files are listed').toBeTruthy();
  const taken = new Set(listed.map((t: any) => `${t.x ?? 0},${t.y ?? 0}`));
  let dx = 0;
  while (taken.has(`${dx},3`)) dx++;

  // Into the directory: a new file at the drop's cell, the home text as it was.
  await gw.copyDragAcrossPanes(a.id, cx, cy, b.id, dx, 3);
  const made = path.join(ROOT, 'Plan.md');
  await expect.poll(() => fs.existsSync(made) && fs.readFileSync(made, 'utf8'), { timeout: 10_000 }).toBe('Plan\n\nsteps');
  await expect
    .poll(async () => tileAt(await gw.getGrid(b.gridID), 'text', dx, 3)?.altText, { timeout: 10_000 })
    .toBe('Plan.md');
  expect(await gw.getTileContent(text.id), 'the home text is untouched').toBe('Plan\n\nsteps');

  // Out of it: the markdown file is a home text holding its bytes. A file
  // never placed sits where the listing derives, so it is read again.
  const relisted = (await gw.getGrid(b.gridID)).tiles ?? [];
  const at = (id: string) => relisted.find((t: any) => t.id === id)!;
  await gw.copyDragAcrossPanes(b.id, Number(at(notes.id).x ?? 0), Number(at(notes.id).y ?? 0), a.id, cx - 1, cy);
  const copy = tileAt(await gw.getGrid(a.gridID), 'text', cx - 1, cy)!;
  expect(copy, 'the markdown file landed a text in home').toBeTruthy();
  expect(copy.link ?? false, 'it copied, not linked').toBe(false);
  expect(await gw.getTileContent(copy.id), 'the copy holds the bytes').toBe('# notes\n\nkept on disk\n');

  // Any other file links: dashed, naming the file.
  await gw.copyDragAcrossPanes(b.id, Number(at(plain.id).x ?? 0), Number(at(plain.id).y ?? 0), a.id, cx + 1, cy);
  const link = tileAt(await gw.getGrid(a.gridID), 'text', cx + 1, cy)!;
  expect(link, 'the .txt landed in home').toBeTruthy();
  expect(link.link, 'it is a link').toBe(true);
  expect(link.linkTargetId, 'to the file').toBe(plain.id);
});
