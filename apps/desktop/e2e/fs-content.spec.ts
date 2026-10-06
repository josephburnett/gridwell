import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// Plain-text files show verbatim, with no markdown mangling, because the plugin
// declares text_presentation "plain". A body read under a stamp the file has
// moved past is read again; caching it at version 0 would hide every later
// edit on disk.

const ROOT = makeRunDir();
// Two plugins go through the FIXTURE form; see framing-stability.spec.ts.
// pages declares no writable, so its notes are the read-only plugin document.
test.use({
  extraPlugins: async ({}, use) => {
    await use([
      { kind: 'fs', name: 'code', config: { root: ROOT } },
      { kind: 'pages', name: 'site', config: {} },
    ]);
  },
});

test('the plugin fs declares no tool it cannot honor (#271)', async ({ gw, window }) => {
  await gw.enterPlugin('code');
  await gw.openPalette();
  await gw.expandPlugins();
  const pal = await window.evaluate(() => (window as any).__gridwellTest.palette());
  const labels = (pal.items ?? []).map((i: any) => i.label);
  expect(labels, 'no dead-end search entry on plugin fs').not.toContain('search');
});

test('an fs grid wears the glyph its plugin declared, not a kind the client knows', async ({ gw }) => {
  // The fs plugin declares glyph "folder" and host_content in its plugin.v1
  // handshake, the adapter stamps both onto every grid it serves, and the
  // client's crumb renders the declared face. Nothing between them knows the
  // word "fs".
  await gw.enterPlugin('code');
  const bar = await gw.bar();
  const chain = bar.segments.filter((s) => s.kind === 'chain');
  const here = chain[chain.length - 1];
  expect(here, 'the fs level has a crumb').toBeTruthy();
  expect(here.glyph, 'the crumb wears the declared folder face').toBe('folder');
  // The level the descent came from is the node's own room, which declares
  // nothing, so it wears the well face.
  expect(chain[0].glyph, 'home is owned content').toBe('well');
});

test('the bar says which directory an fs grid lists, at its door and a level down', async ({ gw }) => {
  // The plugin's listing names its source (ListResponse.source_label), the
  // adapter carries it on the grid, and the bar draws it after the room's name.
  fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
  // The plugin may name the root as configured or with its links resolved.
  const endsIn = (d: string) => {
    const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return new RegExp(` — (${esc(d)}|${esc(fs.realpathSync(d))})$`);
  };
  await gw.enterPlugin('code');
  await expect.poll(async () => (await gw.barName()).label, { timeout: 10_000 }).toMatch(endsIn(ROOT));
  const f = await gw.focused();
  const sub = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t) => t.altText === 'sub')!;
  expect(sub, 'sub listed as a well').toBeTruthy();
  await gw.descendCell(Number(sub.x ?? 0), Number(sub.y ?? 0));
  const label = async () => (await gw.barName()).label;
  await expect.poll(label, { timeout: 10_000 }).toMatch(endsIn(path.join(ROOT, 'sub')));
  expect(await label(), 'the room is named before its source').toMatch(/^sub — /);
});

test('a source file shows as plain text and refreshes each open', async ({ gw, window }) => {
  // The freshness half of this test mutates the file and the module-scoped dir
  // persists across runs, so rewrite it first.
  fs.writeFileSync(path.join(ROOT, 'notes.go'), '# not a heading\nplain body v1\n');
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const tile = (snap.tiles ?? []).find((t) => t.altText === 'notes.go')!;
  expect(tile, 'notes.go listed').toBeTruthy();
  expect((tile as { textPresentation?: string }).textPresentation, 'plugin declares plain').toBe('plain');

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
  // The plugin tells the entry changed (EntryChanged) under a new stamp, the
  // node tells the client its row's bytes moved (Tile.content_stamp), and the
  // cache drops the clean body the open view draws, so the view reads it
  // again. The row's version does not move: a plugin body has none.
  const file = path.join(ROOT, 'live.go');
  fs.writeFileSync(file, 'first body\n');
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const tile = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t) => t.altText === 'live.go')!;
  expect(tile, 'live.go listed').toBeTruthy();
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  const shown = async () => (await gw.textareaValue()) ?? '';
  await expect.poll(shown, { timeout: 10_000 }).toContain('first body');

  fs.writeFileSync(file, 'second body, written while open\n');
  await expect.poll(shown, { timeout: 10_000 }).toContain('second body, written while open');
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

  // A same-grid left-drag is placement rather than creation, so the
  // projection, which accepts no tiles, accepts it and its store persists it.
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

  // Resize goes through the same placement door. Park the tile at a known
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

test('a read-only plugin document is selectable, and stays so through a reload (#268)', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('site');
  const f = await gw.focused();
  const tile = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'about')!;
  expect(tile, 'the about note listed').toBeTruthy();
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');

  // The descent must reach the url through the completion write, since a
  // read-only document has no textarea events to stand in for a missing one.
  // The reload must then restore the rendered DOM face, because the
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
    .toContain('serves web pages');

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
    .toContain('plugin');
});
