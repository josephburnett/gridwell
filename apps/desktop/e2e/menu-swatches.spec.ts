import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { makeRunDir } from './homes';

// The + menu is the namespace surface:
//   - boot lands on the node's own home, with "/" as its url;
//   - home, every files root and every connection is a swatch on the menu's
//     top row, above the primitives;
//   - clicking a swatch descends into it, and the ascent returns exactly to
//     where the menu was opened;
//   - dragging a swatch into a grid drops a dashed door to another namespace link.
//
// These behaviors cross the seam from wasm to server, through menuItems,
// descend and CreateDoor, the home store, and SQLite, and no unit layer sees the
// composition.

const DOCS = makeRunDir();
test.use({ extraNodes: ['second'], filesRoots: [{ root: DOCS, label: 'docs' }] });

test('boot lands on home with "/" as its URL', async ({ gw, window }) => {
  const rows = await gw.sources();
  const f = await gw.focused();
  expect(f.gridID, "boot anchors at the node's home").toBe(rows[0].rootGridID);

  await gw.waitIdle();
  const url = await window.evaluate(() => location.pathname + location.search);
  expect(url, 'home keeps "/" as its URL (no a= anchor)').not.toContain('a=');
});

test('a connection anchor rides in the URL path and survives a reload (issue #193)', async ({ gw, window }) => {
  // The anchor is leading path segments, /<node>/<connection>/<grid>. The pane's
  // qualified anchor drops verbatim into the path, and a reload decodes it back
  // to the same place.
  await gw.enterSource('second');
  const before = await gw.focused();
  expect(before.anchor, 'anchored inside the second node').not.toBe('');

  // The url update is debounced, so poll for the settled form.
  await expect
    .poll(async () => window.evaluate(() => location.pathname + location.search))
    .toContain('/' + before.anchor);
  const url = await window.evaluate(() => location.pathname + location.search);
  expect(url, 'no legacy a= anchor param').not.toContain('a=');

  // The wasm re-fetch is slow under full-suite load, so give the hook time.
  await window.evaluate(() => location.reload());
  await window.waitForFunction(() => (window as any).__gridwellTest !== undefined, null, {
    timeout: 45_000,
  });
  // The anchor resolves asynchronously after the hook installs. The fixture's
  // ready-wait covers the first boot; a mid-test reload re-runs boot and needs
  // the same wait.
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

test('swatches fill the + menu top row above the primitives', async ({ gw }) => {
  await gw.sources();
  await gw.openMenu();
  await gw.expandSources();
  const menu = await gw.menu();

  const swatches = menu.items.filter((i) => i.isSwatch);
  const primitives = menu.items.filter((i) => !i.strip);
  // One swatch per declared door. The node's home and the connection's far
  // home are places and get a row each; the files are not a place, and each
  // root they declare is a swatch of its own. Home, then the files, then the
  // connections, and every declared grid rides directly after the row that
  // declared it, named grid first (door.GridName).
  const rows = swatches.filter((i) => !i.sourceGrid);
  expect(rows.map((i) => i.label), 'the two homes').toEqual(['home', 'second']);
  expect(
    swatches.map((i) => i.label),
    'each declared grid rides after its row, named for it',
  ).toEqual(['home', 'trash (home)', 'docs (files)', 'second']);
  expect(primitives.length, 'the primitive swatches are still there').toBeGreaterThanOrEqual(5);

  const strip = menu.items.filter((i) => i.strip).length;
  expect(menu.items.slice(0, strip).every((i) => i.strip), 'the strip comes first').toBe(true);
  expect(menu.items.slice(strip - swatches.length, strip).every((i) => i.isSwatch), 'the swatches end it').toBe(true);
  const swatchRowY = Math.max(...swatches.map((i) => i.y));
  const primitiveRowY = Math.min(...primitives.map((i) => i.y));
  expect(swatchRowY, 'the swatch row is above the primitive row').toBeLessThan(primitiveRowY);
});

test('clicking a swatch descends; ascent returns to where the menu was', async ({ gw }) => {
  const rows = await gw.sources();
  const second = rows.find((p) => p.label === 'second')!;
  const before = await gw.focused();

  await gw.clickSwatch('second');
  const inside = await gw.focused();
  expect(inside.gridID, 'portaled into the second node root').toBe(second.rootGridID);
  expect(inside.placeDepth, 'one frame for the return trip').toBe(1);

  await gw.ascendViaCrumb();
  const back = await gw.focused();
  expect(back.gridID, 'ascent returns to the origin grid').toBe(before.gridID);
  expect(back.placeDepth, 'the frame was consumed').toBe(0);
  expect(back.cx, 'viewport x preserved').toBeCloseTo(before.cx, 1);
  expect(back.cy, 'viewport y preserved').toBeCloseTo(before.cy, 1);
});

test('dragging a swatch into the grid drops a dashed link', async ({ gw }) => {
  const rows = await gw.sources();
  const second = rows.find((p) => p.label === 'second')!;
  const f = await gw.focused();
  const cx = Math.round(f.cx) + 1;
  const cy = Math.round(f.cy) + 1;

  await gw.openMenu();
  await gw.dragSourceLink('second', cx, cy);

  const link = tileAt(await gw.getGrid(f.gridID), 'door', cx, cy)!;
  expect(link, 'door to another namespace link created at the drop cell').toBeTruthy();
  expect(link.childGridId, "child is the far home's qualified root").toBe(second.rootGridID);
  expect(link.link, 'renders dashed (a link, delete only unlinks)').toBe(true);
  expect(link.altText, 'labeled with the connection name').toBe('second');
});

// One glyph per menu row, wherever it is drawn: the + menu swatch and the bar's
// crumb for the grid that row roots read the same owner (door.RowGlyph and
// door.GlyphFor). A connection wears the globe, a files root the folder, and
// home the door. A unit test on either side alone cannot see the two draw
// paths agree.
test('a swatch wears the same glyph in the bar: a globe for a connection, a folder for a root', async ({
  gw,
}) => {
  await gw.enterSource('home');
  await gw.openMenu();
  await gw.expandSources();
  const rows = (await gw.menu()).items.filter((i) => i.isSwatch && i.rootGridID);
  const home = rows.find((r) => r.label === 'home')!;
  const conn = rows.find((r) => r.connection)!;
  const docs = rows.find((r) => r.label === 'docs (files)')!;
  expect(home, 'home has a menu row').toBeTruthy();
  expect(conn, 'the second node is a connection row').toBeTruthy();
  expect(docs, 'the files root has a swatch').toBeTruthy();
  expect(conn.glyph, 'a connection wears the globe').toBe('globe');
  expect(home.glyph, 'home wears the door').toBe('door');
  expect(docs.glyph, 'a files root wears the folder').toBe('folder');

  // The innermost chain crumb is the grid the pane stands on; the leading
  // close-all crumb is not one of the pane's own.
  const chainGlyph = async (): Promise<{ anchor?: string; glyph?: string }> => {
    const chain = (await gw.bar()).segments.filter((s) => s.kind === 'chain' && !s.closeOnly);
    return chain[chain.length - 1];
  };
  const atHome = await chainGlyph();
  expect(atHome.anchor).toBe(home.rootGridID);
  expect(atHome.glyph, "the bar wears the home swatch's glyph").toBe(home.glyph);

  await gw.clickSwatch('second');
  const atConn = await chainGlyph();
  expect(atConn.anchor).toBe(conn.rootGridID);
  expect(atConn.glyph, "the bar wears the connection swatch's glyph").toBe(conn.glyph);

  await gw.ascendViaCrumb();
  await gw.clickSwatch('docs (files)');
  const atDocs = await chainGlyph();
  expect(atDocs.anchor).toBe(docs.rootGridID);
  expect(atDocs.glyph, "the bar wears the root swatch's glyph").toBe('folder');
});
