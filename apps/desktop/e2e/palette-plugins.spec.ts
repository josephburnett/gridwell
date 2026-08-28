import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The + menu is the plugin surface (owner decision 2026-07-19, reversing the
// launcher-as-landing-page decision of PR #41):
//   - boot lands on the FIRST plugin in server.yaml, with "/" as its URL;
//   - every configured plugin is a swatch on the + menu's TOP row, above the
//     primitives;
//   - clicking a plugin swatch descends into it (a portal — ascent returns
//     exactly to where the menu was opened);
//   - dragging a plugin swatch into a grid drops a dashed exit-well link.
//
// Why is this spec here? The three behaviors cross the wasm↔server seam
// (paletteItems → startDescent/CreateWell → localdb → SQLite) and none of the
// unit layers can see the composition.

test.use({ extraPlugins: [{ kind: 'home', name: 'second' }] });

test('boot lands on the first plugin with "/" as its URL', async ({ gw, window }) => {
  const pls = await gw.plugins();
  const f = await gw.focused();
  expect(f.gridID, 'boot anchors at the FIRST configured plugin root').toBe(pls[0].rootGridID);
  expect(f.anchor, 'not the node grid').not.toMatch(/\/0$/);

  // Home encodes as a bare URL: no anchor param, root path.
  await gw.waitIdle();
  const url = await window.evaluate(() => location.pathname + location.search);
  expect(url, 'home keeps "/" as its URL (no a= anchor)').not.toContain('a=');
});

test('a plugin anchor rides in the URL path and survives a reload (issue #193)', async ({ gw, window }) => {
  // 2026-07-25: the anchor is leading PATH segments — /<plugin-id>/<grid> —
  // not an a= query param. The pane's qualified anchor drops verbatim into
  // the path, and a reload decodes it back to the same place.
  await gw.enterPlugin('second');
  const before = await gw.focused();
  expect(before.anchor, 'anchored inside the second plugin').not.toBe('');

  // The URL update is debounced; poll for the settled form.
  await expect
    .poll(async () => window.evaluate(() => location.pathname + location.search))
    .toContain('/' + before.anchor);
  const url = await window.evaluate(() => location.pathname + location.search);
  expect(url, 'no legacy a= anchor param').not.toContain('a=');

  // Reload: the new-form URL must decode back into the same anchor. The
  // wasm re-fetch is slow under full-suite load — give the hook time.
  await window.evaluate(() => location.reload());
  await window.waitForFunction(() => (window as any).__gridwellTest !== undefined, null, {
    timeout: 45_000,
  });
  // Boot isn't done at hook-install: the anchor resolves asynchronously
  // (the fixture's ready-wait covers first boot; a mid-test reload re-runs
  // boot without it — same race, same wait).
  await window.waitForFunction(
    () => {
      const t = (window as any).__gridwellTest;
      try {
        return (t.panes() as Array<{ focused: boolean; anchor: string }>).some(
          (p) => p.focused && p.anchor !== '',
        );
      } catch {
        return false;
      }
    },
    null,
    { timeout: 45_000 },
  );
  await gw.waitIdle();
  const after = await gw.focused();
  expect(after.anchor, 'reload decoded the path anchor').toBe(before.anchor);
  expect(after.gridID, 'landed on the same grid').toBe(before.gridID);
});

test('plugins fill the + menu top row above the primitives', async ({ gw }) => {
  await gw.plugins();
  await gw.openPalette();
  const pal = await gw.palette();

  const plugins = pal.items.filter((i) => i.isPlugin);
  const primitives = pal.items.filter((i) => !i.isPlugin);
  // Plugin rows in server.yaml order; each local plugin's declared TRASH
  // root entry (#262) rides directly after its row.
  const rows = plugins.filter((i) => !i.entry);
  expect(rows.map((i) => i.label), 'both plugins, server.yaml order').toEqual(['e2e', 'second']);
  expect(
    plugins.map((i) => i.label),
    'each root entry rides after its declaring plugin',
  ).toEqual(['e2e', 'trash', 'second', 'trash']);
  expect(primitives.length, 'the primitive swatches are still there').toBeGreaterThanOrEqual(5);

  // Plugins come first in index order and sit strictly ABOVE the primitives.
  expect(pal.items.slice(0, plugins.length).every((i) => i.isPlugin)).toBe(true);
  const pluginRowY = Math.max(...plugins.map((i) => i.y));
  const primitiveRowY = Math.min(...primitives.map((i) => i.y));
  expect(pluginRowY, 'plugin row is above the primitive row').toBeLessThan(primitiveRowY);
});

test('clicking a plugin swatch descends; ascent returns to where the menu was', async ({ gw }) => {
  const pls = await gw.plugins();
  const second = pls.find((p) => p.label === 'second')!;
  const before = await gw.focused();

  await gw.clickPluginSwatch('second');
  const inside = await gw.focused();
  expect(inside.gridID, 'portaled into the second plugin root').toBe(second.rootGridID);
  expect(inside.frameDepth, 'one portal frame for the return trip').toBe(1);

  // Ascend: back exactly where the menu was opened.
  await gw.ascendViaCrumb();
  const back = await gw.focused();
  expect(back.gridID, 'ascent returns to the origin grid').toBe(before.gridID);
  expect(back.frameDepth, 'the frame was consumed').toBe(0);
  expect(back.cx, 'viewport x preserved').toBeCloseTo(before.cx, 1);
  expect(back.cy, 'viewport y preserved').toBeCloseTo(before.cy, 1);
});

test('dragging a plugin swatch into the grid drops a dashed link', async ({ gw }) => {
  const pls = await gw.plugins();
  const second = pls.find((p) => p.label === 'second')!;
  const f = await gw.focused();
  const cx = Math.round(f.cx) + 1;
  const cy = Math.round(f.cy) + 1;

  await gw.openPalette();
  await gw.dragPluginLink('second', cx, cy);

  const link = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  expect(link, 'exit-well link created at the drop cell').toBeTruthy();
  expect(link.childGridId, "child is the plugin's qualified root").toBe(second.rootGridID);
  expect(link.reference, 'renders dashed (a link, delete only unlinks)').toBe(true);
  expect(link.altText, 'labeled with the plugin name').toBe('second');
});
