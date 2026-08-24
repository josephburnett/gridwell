import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// fs content-types program (decisions 2026-08-13): plain-text files show
// VERBATIM (no markdown mangling — text_presentation "plain", declared by
// the plugin), and a read-only body REFRESHES on every descent (it used
// to cache at version 0 forever, so edits on disk never appeared).

const ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-fscontent-'));
test.use({ extraPlugins: [{ kind: 'fs', name: 'code', config: { root: ROOT } }] });

test('the provider fs declares no tool it cannot honor (#271)', async ({ gw, window }) => {
  await gw.enterPlugin('code');
  await gw.openPalette();
  const pal = await window.evaluate(() => (window as any).__gridwellTest.palette());
  const labels = (pal.items ?? []).map((i: any) => i.label);
  expect(labels, 'no dead-end search entry on provider fs').not.toContain('search');
});

test('a source file shows as plain text and refreshes each open', async ({ gw, window }) => {
  // Reset the fixture: this test MUTATES the file (the freshness half),
  // and the module-scoped dir persists across runs.
  fs.writeFileSync(path.join(ROOT, 'notes.go'), '# not a heading\nplain body v1\n');
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const tile = (snap.tiles ?? []).find((t) => t.altText === 'notes.go')!;
  expect(tile, 'notes.go listed').toBeTruthy();
  expect((tile as { textPresentation?: string }).textPresentation, 'plugin declares plain').toBe('plain');

  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  // Verbatim: the '#' line is NOT a heading; the body sits in the plain
  // <pre>, and no toggle button offers a markdown flip.
  await expect
    .poll(() =>
      window.evaluate(() => document.getElementById('gw-rendered-view')?.innerHTML ?? ''),
    )
    .toContain('gw-plain');
  const html = await window.evaluate(() => document.getElementById('gw-rendered-view')!.innerHTML);
  expect(html).toContain('# not a heading');
  expect(html).not.toContain('<h1');
  expect(
    await window.evaluate(() => (document.getElementById('gw-text-toggle') as HTMLElement)?.style.display),
    'no rendered/raw toggle for a plain declaration',
  ).toBe('none');

  // Freshness: change the file on disk, leave, come back — the new bytes
  // show (each open re-reads; it is all read-only).
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  fs.writeFileSync(path.join(ROOT, 'notes.go'), 'plain body v2 — changed on disk\n');
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect
    .poll(() =>
      window.evaluate(() => document.getElementById('gw-rendered-view')?.textContent ?? ''),
      { timeout: 10_000 },
    )
    .toContain('changed on disk');
});

test('a projection rearranged stays rearranged: fs tiles move and resize (#266)', async ({
  gw,
}) => {
  fs.mkdirSync(path.join(ROOT, 'movedir'), { recursive: true });
  fs.writeFileSync(path.join(ROOT, 'sizeme.md'), 'a body to size\n');
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const dir = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'movedir')!;
  expect(dir, 'movedir listed').toBeTruthy();

  // MOVE: a same-grid left-drag is placement, not creation — the read-only
  // (writable=false) projection accepts it and its store persists it. The
  // client used to reject this before the RPC could even fire.
  const fx = Number(dir.x ?? 0);
  const fy = Number(dir.y ?? 0);
  await gw.dragTileCell(fx, fy, fx, fy + 2);
  await expect
    .poll(async () => {
      const t = (await gw.getGrid(f.gridID)).tiles!.find((x) => x.altText === 'movedir')!;
      return `${Number(t.x ?? 0)},${Number(t.y ?? 0)}`;
    })
    .toBe(`${fx},${fy + 2}`);

  // RESIZE persists too (a file tile, same placement door). Park it at a
  // known in-viewport cell first; the +2 target grows one cell (the
  // moving corner snaps like tile-gestures.spec pins).
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
  fs.writeFileSync(path.join(ROOT, 'copyme.txt'), 'grab these words with the mouse\n');
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const tile = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'copyme.txt')!;
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');

  // RELOAD lands back inside the descent. Two halves of the same promise:
  // the descent must reach the URL at all (the completion write — a
  // read-only file has no textarea events to paper over the missing one),
  // and the restore must come back on the rendered (DOM) face, not
  // canvas-drawn "text" mode with nothing to select.
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
    .toContain('grab these words');

  // A REAL mouse drag across the text selects it — the end-to-end claim
  // (no handler may swallow the drag, no user-select may block it).
  const box = await window.evaluate(() => {
    const pre = document.querySelector('#gw-rendered-view pre.gw-plain')!;
    const r = pre.getBoundingClientRect();
    return { x: r.x, y: r.y, w: r.width, h: r.height };
  });
  await window.mouse.move(box.x + 2, box.y + 8);
  await window.mouse.down();
  await window.mouse.move(box.x + Math.min(box.w - 4, 300), box.y + 8, { steps: 8 });
  await window.mouse.up();
  await expect
    .poll(() => window.evaluate(() => window.getSelection()?.toString() ?? ''))
    .toContain('grab');
});
