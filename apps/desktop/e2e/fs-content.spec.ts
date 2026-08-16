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
