import { test, expect } from './fixtures';
import { tileAt, getGrid } from '../e2e/oracle';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// Plugin-declared menu entries (#258), end to end through the wire: fs
// declares one CREATION entry ("search"); the (+) menu shows it inside fs
// grids even though the grid is read-only (the declaration IS the
// permission); the drop mints the tool tile with NO prompt (#209's
// drop-first rule); the first descent prompts for the query; the commit
// fills the child grid with LINK tiles into the real fs id space. The
// host never learns what "search" means — every step rides Info
// menu_entries → Grid stamping → Tile.menu_entry → WriteContent.

const ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-web-search-'));
fs.writeFileSync(path.join(ROOT, 'alpha-notes.txt'), 'a\n');
fs.mkdirSync(path.join(ROOT, 'sub'));
fs.writeFileSync(path.join(ROOT, 'sub', 'alpha-deep.txt'), 'b\n');
fs.writeFileSync(path.join(ROOT, 'sub', 'beta.txt'), 'c\n');

// legacy: the #258 tool flow lives only on the pre-v2 fs plugin until
// the userdocs store lands (#271) — the adapter strips creation entries
// it cannot honor.
test.use({ extraPlugins: [{ kind: 'fs', name: 'docs', config: { root: ROOT }, legacy: true }] });

test('the fs search entry: drop, prompt on descent, results grid of links', async ({
  gw,
  window,
  serve,
}) => {
  await gw.enterPlugin('docs');
  const f = await gw.focused();
  // Pick a free cell near the viewport center (the projected files occupy
  // their reconciled cells; the drop needs an empty one inside the pane).
  const seed = await gw.getGrid(f.gridID);
  const used = new Set((seed.tiles ?? []).map((t) => `${t.x ?? 0},${t.y ?? 0}`));
  let cx = Math.round(f.cx);
  let cy = Math.round(f.cy);
  outer: for (const dy of [0, 1, -1]) {
    for (const dx of [0, 1, -1]) {
      if (!used.has(`${Math.round(f.cx) + dx},${Math.round(f.cy) + dy}`)) {
        cx = Math.round(f.cx) + dx;
        cy = Math.round(f.cy) + dy;
        break outer;
      }
    }
  }

  // The entry swatch rides the palette INSIDE an fs grid, below the
  // primitives (which fs, being read-only, does not offer).
  await gw.openPalette();
  const pal = await gw.palette();
  expect(
    pal.items.some((i) => !i.isPlugin && i.entry === 'search'),
    'the fs grid palette offers the declared search entry',
  ).toBe(true);

  // Drop: mints the tool tile immediately — no prompt at drop time.
  await gw.dragEntryCreate('search', cx, cy);
  const snap = await gw.getGrid(f.gridID);
  const well = tileAt(snap, 'well', cx, cy)!;
  expect(well, 'the drop minted a well in the fs grid').toBeTruthy();
  expect(well.menuEntry, 'the tile carries its entry id on the wire').toBe('search');
  expect(well.childGridId ?? '', 'no child before the query commits').toBe('');

  // First descent: the params form (the entry's declared schema), centered
  // on the pane — not a descent yet.
  await gw.descendCell(cx, cy);
  const form = window.locator('#gw-entry-form');
  await expect(form, 'descent prompts for the entry params').toBeVisible();
  await form.locator('input[name="query"]').fill('alpha');
  await form.locator('#gw-entry-ok').click();

  // The commit runs the search and the descent completes into the results.
  await expect
    .poll(async () => (await gw.focused()).gridID, { timeout: 10_000 })
    .not.toBe(f.gridID);
  const results = await gw.focused();

  // The results are LINKS into the real fs id space: two alpha files, each
  // a leaf link (reference on the wire) whose target resolves to the real
  // projected tile — a copy carried anywhere keeps routing.
  const rg = await getGrid(serve.origin, results.gridID);
  const names = (rg.tiles ?? []).map((t) => t.altText).sort();
  expect(names, 'both alpha files, never beta').toEqual(['alpha-notes.txt', 'sub/alpha-deep.txt']);
  for (const t of rg.tiles ?? []) {
    expect(t.linkTargetId, `${t.altText} is a leaf link`).toBeTruthy();
    expect(t.reference, `${t.altText} reads as a link`).toBe(true);
  }

  // Ascend: the tool tile now wears its committed face.
  await gw.ascendViaCrumb();
  const after = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  expect(after.altText, 'the query is the tile face').toBe('search: alpha');
  expect(after.childGridId, 'the snapshot grid is the child').toBe(results.gridID);
});
