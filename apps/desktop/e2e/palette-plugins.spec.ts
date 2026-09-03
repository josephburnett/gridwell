import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The + menu is the namespace surface:
//   - boot lands on the node's own home, with "/" as its url;
//   - every configured plugin and connection is a swatch on the menu's top row,
//     above the primitives;
//   - clicking a swatch descends into it, and the ascent returns exactly to
//     where the menu was opened;
//   - dragging a swatch into a grid drops a dashed exit-well link.
//
// These behaviors cross the seam from wasm to server, through paletteItems,
// descend and CreateWell, the home store, and SQLite, and no unit layer sees the
// composition.

test.use({ extraNodes: ['second'] });

test('boot lands on the first plugin with "/" as its URL', async ({ gw, window }) => {
  const pls = await gw.plugins();
  const f = await gw.focused();
  expect(f.gridID, 'boot anchors at the FIRST configured plugin root').toBe(pls[0].rootGridID);

  // Home encodes as a bare url: no anchor param, root path.
  await gw.waitIdle();
  const url = await window.evaluate(() => location.pathname + location.search);
  expect(url, 'home keeps "/" as its URL (no a= anchor)').not.toContain('a=');
});

test('a plugin anchor rides in the URL path and survives a reload (issue #193)', async ({ gw, window }) => {
  // The anchor is leading path segments, /<plugin-id>/<grid>, not an a= query
  // param. The pane's qualified anchor drops verbatim into the path, and a
  // reload decodes it back to the same place.
  await gw.enterPlugin('second');
  const before = await gw.focused();
  expect(before.anchor, 'anchored inside the second plugin').not.toBe('');

  // The url update is debounced, so poll for the settled form.
  await expect
    .poll(async () => window.evaluate(() => location.pathname + location.search))
    .toContain('/' + before.anchor);
  const url = await window.evaluate(() => location.pathname + location.search);
  expect(url, 'no legacy a= anchor param').not.toContain('a=');

  // Reload: the url must decode back into the same anchor. The wasm re-fetch is
  // slow under full-suite load, so give the hook time.
  await window.evaluate(() => location.reload());
  await window.waitForFunction(() => (window as any).__gridwellTest !== undefined, null, {
    timeout: 45_000,
  });
  // Boot is not done at hook-install: the anchor resolves asynchronously. The
  // fixture's ready-wait covers the first boot, and a mid-test reload re-runs
  // boot without it, so it needs the same wait.
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
  // Rows in server.yaml order; each local plugin's declared trash root entry
  // rides directly after its row.
  const rows = plugins.filter((i) => !i.entry);
  expect(rows.map((i) => i.label), 'both plugins, server.yaml order').toEqual(['home', 'second']);
  expect(
    plugins.map((i) => i.label),
    'each root entry rides after its declaring plugin',
  ).toEqual(['home', 'trash', 'second']);
  expect(primitives.length, 'the primitive swatches are still there').toBeGreaterThanOrEqual(5);

  // Plugins come first in index order and sit strictly above the primitives.
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
  expect(inside.placeDepth, 'one frame for the return trip').toBe(1);

  // Ascend: back exactly where the menu was opened.
  await gw.ascendViaCrumb();
  const back = await gw.focused();
  expect(back.gridID, 'ascent returns to the origin grid').toBe(before.gridID);
  expect(back.placeDepth, 'the frame was consumed').toBe(0);
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

// One face per menu row, wherever it is drawn: the + menu swatch and the bar's
// crumb for the grid that row roots read the same owner (door.RowGlyph /
// door.GlyphFor). Globes are for connections; grids are for plugins, a plugin
// that declares no glyph at all included. A unit test on either side alone
// cannot see this — the disagreement was between the menu's draw call, which
// defaulted an undeclared glyph to the globe, and the crumb's rule, which
// defaulted it to the grid face.
test('a menu row wears the same face in the bar: a globe for a connection, a grid for a plugin', async ({
  gw,
}) => {
  await gw.enterPlugin('home');
  await gw.openPalette();
  const rows = (await gw.palette()).items.filter((i) => i.isPlugin && i.rootGridID);
  const home = rows.find((r) => r.label === 'home' || r.kind === 'home')!;
  const conn = rows.find((r) => r.kind === 'connection')!;
  expect(home, 'the home plugin has a menu row').toBeTruthy();
  expect(conn, 'the second node is a connection row').toBeTruthy();
  expect(conn.glyph, 'a connection wears the globe').toBe('globe');
  expect(home.glyph, 'a plugin wears a grid face, never the globe').not.toBe('globe');

  // The innermost chain crumb is the grid the pane stands on; the leading
  // close-all crumb is not one of the pane's own.
  const chainFace = async (): Promise<{ anchor?: string; glyph?: string }> => {
    const chain = (await gw.bar()).segments.filter((s) => s.kind === 'chain' && !s.closeOnly);
    return chain[chain.length - 1];
  };
  const atHome = await chainFace();
  expect(atHome.anchor).toBe(home.rootGridID);
  expect(atHome.glyph, "the bar wears the home swatch's face").toBe(home.glyph);

  // The connection's root, one descent away.
  await gw.clickPluginSwatch('second');
  const atConn = await chainFace();
  expect(atConn.anchor).toBe(conn.rootGridID);
  expect(atConn.glyph, "the bar wears the connection swatch's face").toBe(conn.glyph);
});
