import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// Plain-text files show verbatim, with no markdown mangling, because the files
// declare text_presentation "plain" by their extension. A body read under a
// stamp the file has moved past is read again; caching it at version 0 would
// hide every later edit on disk.

const ROOT = makeRunDir();
test.use({ filesRoots: [{ root: ROOT, label: 'code' }] });

test('a directory grid wears the folder glyph the files declare', async ({ gw }) => {
  // The files stamp glyph "folder" onto every grid they serve, and the
  // client's crumb renders the declared glyph.
  await gw.enterSource('code (files)');
  const bar = await gw.bar();
  const chain = bar.segments.filter((s) => s.kind === 'chain');
  const here = chain[chain.length - 1];
  expect(here, 'the directory level has a crumb').toBeTruthy();
  expect(here.glyph, 'the crumb wears the declared folder glyph').toBe('folder');
  // The level the descent came from is the node's own grid, which declares
  // nothing, so it wears the door glyph.
  expect(chain[0].glyph, 'home is owned content').toBe('door');
});

test('the bar says which directory a grid lists, at its root and a level down', async ({ gw }) => {
  // The grid's id is the directory's path, and the bar draws that path after
  // the grid's name (bartitle.Input.DirectoryPath).
  fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
  // The root may be named as configured or with its links resolved.
  const endsIn = (d: string) => {
    const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return new RegExp(` — (${esc(d)}|${esc(fs.realpathSync(d))})$`);
  };
  await gw.enterSource('code (files)');
  await expect.poll(async () => (await gw.barName()).label, { timeout: 10_000 }).toMatch(endsIn(ROOT));
  const f = await gw.focused();
  const sub = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t) => t.altText === 'sub')!;
  expect(sub, 'sub listed as a door').toBeTruthy();
  await gw.descendCell(Number(sub.x ?? 0), Number(sub.y ?? 0));
  const label = async () => (await gw.barName()).label;
  await expect.poll(label, { timeout: 10_000 }).toMatch(endsIn(path.join(ROOT, 'sub')));
  expect(await label(), 'the grid is named before its path').toMatch(/^sub — /);
});

test('a source file shows as plain text and refreshes each open', async ({ gw, window }) => {
  // The freshness half of this test mutates the file and the module-scoped dir
  // persists across runs, so rewrite it first.
  fs.writeFileSync(path.join(ROOT, 'notes.go'), '# not a heading\nplain body v1\n');
  await gw.enterSource('code (files)');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const tile = (snap.tiles ?? []).find((t) => t.altText === 'notes.go')!;
  expect(tile, 'notes.go listed').toBeTruthy();
  expect((tile as { textPresentation?: string }).textPresentation, 'a .go file is plain').toBe('plain');

  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  // The '#' line is the file's own bytes in the editor, and no toggle offers
  // a markdown flip.
  await expect.poll(async () => gw.textareaValue()).toBe('# not a heading\nplain body v1\n');
  expect(
    await window.evaluate(() => (document.getElementById('gw-text-toggle') as HTMLElement)?.style.display),
    'no rendered/raw toggle for a plain declaration',
  ).toBe('none');

  // Bytes changed on disk while the file is closed show on the next descent:
  // the row's stamp moved past the body's.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  fs.writeFileSync(path.join(ROOT, 'notes.go'), 'plain body v2 — changed on disk\n');
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect
    .poll(async () => gw.textareaValue(), { timeout: 10_000 })
    .toBe('plain body v2 — changed on disk\n');
});

test('a file open in a pane shows bytes written on disk with no gesture', async ({ gw }) => {
  // The watch tells the file changed under a new stamp (Tile.content_stamp),
  // and the cache drops the clean body the open view draws, so the view reads
  // it again. The row's version does not move: a file's body has none.
  const file = path.join(ROOT, 'live.go');
  fs.writeFileSync(file, 'first body\n');
  await gw.enterSource('code (files)');
  const f = await gw.focused();
  const tile = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t) => t.altText === 'live.go')!;
  expect(tile, 'live.go listed').toBeTruthy();
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  const shown = async () => (await gw.textareaValue()) ?? '';
  await expect.poll(shown, { timeout: 10_000 }).toContain('first body');

  fs.writeFileSync(file, 'second body, written while open\n');
  await expect.poll(shown, { timeout: 10_000 }).toContain('second body, written while open');
});

test('a directory rearranged stays rearranged: its tiles move and resize (#266)', async ({
  gw,
}) => {
  fs.mkdirSync(path.join(ROOT, 'movedir'), { recursive: true });
  fs.writeFileSync(path.join(ROOT, 'sizeme.md'), 'a body to size\n');
  await gw.enterSource('code (files)');
  const f = await gw.focused();
  const dir = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'movedir')!;
  expect(dir, 'movedir listed').toBeTruthy();

  // A same-grid left-drag is placement rather than creation, so the
  // directory, which accepts no tiles, accepts it and the store persists it.
  // The client must not refuse the gesture before the RPC can fire.
  const fx = Number(dir.x ?? 0);
  const fy = Number(dir.y ?? 0);
  await gw.dragTileCell(fx, fy, fx, fy + 2);
  await expect
    .poll(async () => {
      const t = (await gw.getGrid(f.gridID)).tiles!.find((x) => x.altText === 'movedir')!;
      return `${Number(t.x ?? 0)},${Number(t.y ?? 0)}`;
    })
    .toBe(`${fx},${fy + 2}`);

  // Resize goes through the same placement write. Park the tile at a known
  // in-viewport cell first; the +2 target grows it one cell, because the moving
  // corner snaps the way tile-gestures.spec pins.
  const file = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'sizeme.md')!;
  await gw.dragTileCell(Number(file.x ?? 0), Number(file.y ?? 0), 0, 1);
  await expect
    .poll(async () => {
      const t = (await gw.getGrid(f.gridID)).tiles!.find((x) => x.altText === 'sizeme.md')!;
      return `${Number(t.x ?? 0)},${Number(t.y ?? 0)}`;
    })
    .toBe('0,1');
  await gw.resizeTileCell(0, 1, 2, 2);
  await expect
    .poll(async () => {
      const t = (await gw.getGrid(f.gridID)).tiles!.find((x) => x.altText === 'sizeme.md')!;
      return `${Number(t.w ?? 0)}x${Number(t.h ?? 0)}`;
    })
    .toBe('2x1');
});

test('a read-only file is selectable, and stays so through a reload (#268)', async ({
  gw,
  window,
}) => {
  test.skip(process.getuid?.() === 0, 'root writes a 0444 file');
  const file = path.join(ROOT, 'about.md');
  if (fs.existsSync(file)) fs.chmodSync(file, 0o644);
  fs.writeFileSync(file, '# About\n\nThis note is read-only on disk, and still selectable.\n');
  fs.chmodSync(file, 0o444);
  await gw.enterSource('code (files)');
  const f = await gw.focused();
  const tile = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'about.md')!;
  expect(tile, 'about.md listed').toBeTruthy();
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');

  // The descent must reach the url through the completion write, since a
  // read-only document has no textarea events to stand in for a missing one.
  // The reload must then restore the rendered DOM view, because the
  // canvas-drawn text mode has nothing to select.
  const fileSeg = String(tile.id).split('/').pop()!;
  await expect
    .poll(() => window.evaluate(() => location.pathname), { timeout: 10_000 })
    .toBe('/' + f.gridID + '/' + fileSeg);
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  await expect
    .poll(
      () =>
        window.evaluate(() => {
          const v = document.getElementById('gw-rendered-view');
          return v && v.style.display !== 'none' ? v.textContent ?? '' : '';
        }),
      { timeout: 15_000 },
    )
    .toContain('read-only on disk');

  // A real mouse drag must select the text, so no handler may swallow the drag
  // and no user-select may block it.
  const box = await window.evaluate(() => {
    const p = document.querySelector('#gw-rendered-view p')!;
    const r = p.getBoundingClientRect();
    return { x: r.x, y: r.y, w: r.width, h: r.height };
  });
  await window.mouse.move(box.x + 2, box.y + 8);
  await window.mouse.down();
  await window.mouse.move(box.x + Math.min(box.w - 4, 300), box.y + 8, { steps: 8 });
  await window.mouse.up();
  await expect
    .poll(() => window.evaluate(() => globalThis.getSelection()?.toString() ?? ''))
    .toContain('read-only');
});
