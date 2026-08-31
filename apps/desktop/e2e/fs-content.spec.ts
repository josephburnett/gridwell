import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// Plain-text files show verbatim, with no markdown mangling, because the plugin
// declares text_presentation "plain". A read-only body refreshes on every
// descent; caching it at version 0 would hide every later edit on disk.

const ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-fscontent-'));
test.use({ extraPlugins: [{ kind: 'fs', name: 'code', config: { root: ROOT } }] });

test('the plugin fs declares no tool it cannot honor (#271)', async ({ gw, window }) => {
  await gw.enterPlugin('code');
  await gw.openPalette();
  const pal = await window.evaluate(() => (window as any).__gridwellTest.palette());
  const labels = (pal.items ?? []).map((i: any) => i.label);
  expect(labels, 'no dead-end search entry on plugin fs').not.toContain('search');
});

test('an fs grid wears the glyph its plugin declared, not a kind the client knows', async ({ gw }) => {
  // The whole declaration flow in one assertion: the fs plugin declares
  // glyph "folder" and host_content in its plugin.v1 handshake, the adapter
  // stamps both onto every grid it serves, and the client's crumb renders the
  // declared face. Nothing between them knows the word "fs" any more — before
  // the declared facts this crumb came from a Grid.source_kind enum the client
  // switched on, so a plugin that was not fs or proc could never wear a face.
  await gw.enterPlugin('code');
  const bar = await gw.bar();
  const chain = bar.segments.filter((s) => s.kind === 'chain');
  const here = chain[chain.length - 1];
  expect(here, 'the fs level has a crumb').toBeTruthy();
  expect(here.glyph, 'the crumb wears the declared folder face').toBe('folder');
  // The level the descent came from is the node's own room: no declaration,
  // so the well face. The two arms of the same rule, one gesture apart.
  expect(chain[0].glyph, 'home is owned content').toBe('well');
});

test('a source file shows as plain text and refreshes each open', async ({ gw, window }) => {
  // Reset the fixture: the freshness half of this test mutates the file, and the
  // module-scoped dir persists across runs.
  fs.writeFileSync(path.join(ROOT, 'notes.go'), '# not a heading\nplain body v1\n');
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const tile = (snap.tiles ?? []).find((t) => t.altText === 'notes.go')!;
  expect(tile, 'notes.go listed').toBeTruthy();
  expect((tile as { textPresentation?: string }).textPresentation, 'plugin declares plain').toBe('plain');

  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  // Verbatim: the '#' line is not a heading, the body sits in a plain <pre>, and
  // no toggle button offers a markdown flip.
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

  // Freshness: change the file on disk, leave, come back, and the new bytes
  // show. Every open re-reads, since it is all read-only.
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

  // Move: a same-grid left-drag is placement, not creation, so the read-only
  // projection accepts it and its store persists it. The client must not refuse
  // the gesture before the RPC can fire.
  const fx = Number(dir.x ?? 0);
  const fy = Number(dir.y ?? 0);
  await gw.dragTileCell(fx, fy, fx, fy + 2);
  await expect
    .poll(async () => {
      const t = (await gw.getGrid(f.gridID)).tiles!.find((x) => x.altText === 'movedir')!;
      return `${Number(t.x ?? 0)},${Number(t.y ?? 0)}`;
    })
    .toBe(`${fx},${fy + 2}`);

  // Resize persists too, through the same placement door. Park the tile at a
  // known in-viewport cell first; the +2 target grows it one cell, since the
  // moving corner snaps the way tile-gestures.spec pins.
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

  // A reload lands back inside the descent. Two halves of one promise: the
  // descent must reach the url at all, through the completion write, since a
  // read-only file has no textarea events to paper over a missing one; and the
  // restore must come back on the rendered DOM face, not the canvas-drawn text
  // mode with nothing to select.
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

  // A real mouse drag across the text selects it: no handler may swallow the
  // drag and no user-select may block it.
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
    .poll(() => window.evaluate(() => globalThis.getSelection()?.toString() ?? ''))
    .toContain('grab');
});
