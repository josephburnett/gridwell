import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A symlink is a link (CLAUDE.md, 2026-11-07). One to a file is a link tile
// naming the file's path; one to a directory is a door onto the target's own
// grid, drawn as a link because the grid is not its own. Either is dashed,
// opens the target, and is dead when the target is gone.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'real'));
fs.writeFileSync(path.join(ROOT, 'real', 'inside.md'), '# inside\n');
fs.writeFileSync(path.join(ROOT, 'target.md'), 'the target body\n');
fs.symlinkSync(path.join(ROOT, 'target.md'), path.join(ROOT, 'alias.md'));
fs.symlinkSync(path.join(ROOT, 'real'), path.join(ROOT, 'shortcut'));
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

const deadLinks = (window: any, gridID: string): Promise<string[]> =>
  window.evaluate((gid: string) => (window as any).__gridwellTest.deadLinks(gid), gridID);

test('a symlink to a file is a link that opens the file, and is dead once the file is gone', async ({ gw, window }) => {
  await gw.enterSource('docs (files)');
  const f = await gw.focused();
  const tiles = (await gw.getGrid(f.gridID)).tiles ?? [];
  const alias = tiles.find((t) => t.altText === 'alias.md')!;
  const target = tiles.find((t) => t.altText === 'target.md')!;
  expect(alias, 'the symlink is listed').toBeTruthy();
  expect(alias.link, 'drawn as a link').toBe(true);
  expect(alias.kind, 'a link to a text file is a text tile').toBe('text');
  expect(alias.linkTargetId, 'naming the target file').toBe(target.id);

  await gw.descendCell(Number(alias.x ?? 0), Number(alias.y ?? 0));
  await expect.poll(() => gw.textareaValue(), { message: "the link opens the target's body", timeout: 10_000 }).toBe(
    'the target body\n',
  );
  await gw.ascendViaCrumb();
  expect(await deadLinks(window, f.gridID), 'a link whose target is there is alive').toEqual([]);

  fs.rmSync(path.join(ROOT, 'target.md'));
  await expect
    .poll(() => deadLinks(window, f.gridID), { message: 'the link reads dead once the target is gone', timeout: 15_000 })
    .toContain(alias.id);
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices, 'and says nothing about it').toEqual([]);
});

test("a symlink to a directory is a door onto the target's grid, drawn as a link", async ({ gw }) => {
  await gw.enterSource('docs (files)');
  const f = await gw.focused();
  const tiles = (await gw.getGrid(f.gridID)).tiles ?? [];
  const shortcut = tiles.find((t) => t.altText === 'shortcut')!;
  const real = tiles.find((t) => t.altText === 'real')!;
  expect(shortcut?.kind, 'the symlink is a door').toBe('door');
  expect(shortcut.link, 'drawn as a link').toBe(true);
  expect(real.link ?? false, 'the directory itself is not').toBe(false);
  expect(shortcut.childGridId, "it opens the target's own grid").toBe(real.childGridId);

  await gw.descendCell(Number(shortcut.x ?? 0), Number(shortcut.y ?? 0));
  await expect.poll(async () => (await gw.focused()).gridID).toBe(real.childGridId);
  expect(
    ((await gw.getGrid(real.childGridId!)).tiles ?? []).map((t) => t.altText),
    "the target's files",
  ).toEqual(['inside.md']);
});
