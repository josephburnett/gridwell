import { test, expect } from './fixtures';

// Locks the descend, reframe, ascend round trip: preview equals descent target
// equals ascent return. This is the invariant behind every "previews go wonky"
// report, and it is the safety net any framing change needs: these tests must
// stay green while the framing writers are consolidated.
//
// The tests drive the real app and read framing from the panes() hook, the live
// viewport the user sees, so they catch a desync between the live pane, the
// saved ascent state, and the server-persisted well framing.
//
// The plugin-root viewport test locks the same invariant at the seam between the
// + menu and a plugin root: enter a plugin from the menu, pan and zoom its root
// grid, ascend, re-enter, and the viewport must be exactly as it was left. A
// menu portal has no containing link tile, so the ascent writeback goes straight
// to the plugin's root framing through SetFraming's root arm. That seam has
// broken before, with every re-entry resetting to the default calibrated zoom,
// so it keeps its own crossing test.

test.use({ extraNodes: ['second'] });

test('re-descending a reframed well returns to exactly what you left', async ({ gw }) => {
  await gw.enterPlugin('home');
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  const childGrid = (await gw.focused()).gridID;

  // Reframe the child grid: zoom in, then pan off-center. The child is empty, so
  // the press lands on no tile and pans.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'the reframe actually changed the zoom').not.toBeCloseTo(1.0, 2);

  // Ascend out, then re-descend: it must land on exactly the framing left behind.
  await gw.middleClickCell(Math.round(left.cx), Math.round(left.cy));
  expect((await gw.focused()).gridID, 'ascended out of the child').not.toBe(childGrid);

  await gw.descendCell(cx, cy);
  const back = await gw.focused();
  expect(back.gridID, 're-descended into the same child grid').toBe(childGrid);
  expect(back.zoom, 'zoom round-tripped').toBeCloseTo(left.zoom, 1);
  expect(back.cx, 'center x round-tripped').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y round-tripped').toBeCloseTo(left.cy, 1);
});

test('plugin root-grid viewport persists across + menu ascent and re-entry', async ({ gw }) => {
  // Enter a plugin from the + menu, reframe its root grid, ascend back home
  // through the portal, re-enter: the viewport must match what was left.
  await gw.enterPlugin('second');
  const pluginGrid = (await gw.focused()).gridID;

  // Reframe the plugin root: zoom in, then pan. The grid is empty, so the press
  // lands on nothing and pans.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'reframe actually changed the zoom').not.toBeCloseTo(1.0, 2);

  // Ascend back home. The menu portal has no containing link tile, so the
  // framing writeback goes straight to the plugin's root view.
  await gw.ascendViaCrumb();
  const home = await gw.focused();
  expect(home.gridID, 'ascended back home').not.toBe(pluginGrid);

  // The write is server truth, not a client cache: the handshake serves each
  // plugin's persisted root view, so the reframed zoom must show up there. Poll,
  // because the root framing post is async.
  await expect
    .poll(
      async () => {
        const pl = (await gw.plugins()).find((x) => x.label === 'second');
        return Number(pl?.rootViewZoom ?? 0);
      },
      { timeout: 5_000 },
    )
    .toBeGreaterThan(0);

  // Re-enter from the menu: the viewport must match what was left.
  await gw.enterPlugin('second');
  const back = await gw.focused();
  expect(back.gridID, 're-entered the same plugin root').toBe(pluginGrid);
  expect(back.zoom, 'zoom restored after re-entry').toBeCloseTo(left.zoom, 1);
  expect(back.cx, 'center x restored after re-entry').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y restored after re-entry').toBeCloseTo(left.cy, 1);
});

test('a reframe persists without ascending (issue #190)', async ({ gw }) => {
  // The settle persister is what makes framing survive leaving a grid any way
  // other than an ascent: a reload, a pane switch, a url edit, a deeper descent.
  // This pins it at the real seam: reframe a child grid and, without ascending,
  // the well's framing must show up in server truth on its own.
  await gw.enterPlugin('home');
  const parentGrid = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  const childGrid = (await gw.focused()).gridID;

  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'the reframe actually changed the zoom').not.toBeCloseTo(1.0, 2);

  // No ascent, no navigation: the debounced settle persister alone must write
  // the framing. A fresh well's zoom is 0 until the first write.
  await expect
    .poll(
      async () => {
        const pg = await gw.getGrid(parentGrid);
        const w = (pg.tiles ?? []).find((t: { childGridId?: string }) => t.childGridId === childGrid);
        return Number((w as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { timeout: 5_000 },
    )
    .toBeGreaterThan(0);
});

test('a plugin root reframe persists without ascending (issue #190)', async ({ gw }) => {
  // The same invariant one seam over: pan and zoom a plugin's root grid, and the
  // root framing must reach the server without a + menu ascent.
  await gw.enterPlugin('second');

  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'reframe actually changed the zoom').not.toBeCloseTo(1.0, 2);

  // The handshake serves each plugin's persisted root view; poll it without
  // ascending.
  await expect
    .poll(
      async () => {
        const pl = (await gw.plugins()).find((x) => x.label === 'second');
        return Number(pl?.rootViewZoom ?? 0);
      },
      { timeout: 5_000 },
    )
    .toBeGreaterThan(0);
});

test('ascending restores the parent viewport unchanged', async ({ gw }) => {
  await gw.enterPlugin('home');
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);

  // The parent (root) framing immediately before descending.
  const before = await gw.focused();

  await gw.descendCell(cx, cy);
  const childGrid = (await gw.focused()).gridID;
  // Reframe the child so ascent has a real framing to write back.
  await gw.wheelAtFocusedCenter(-200);
  const inside = await gw.focused();
  await gw.middleClickCell(Math.round(inside.cx), Math.round(inside.cy));

  const after = await gw.focused();
  expect(after.gridID, 'back in the parent grid').not.toBe(childGrid);
  expect(after.zoom, 'parent zoom unchanged by the descent excursion').toBeCloseTo(before.zoom, 1);
  expect(after.cx, 'parent center x unchanged').toBeCloseTo(before.cx, 1);
  expect(after.cy, 'parent center y unchanged').toBeCloseTo(before.cy, 1);
});

// A fresh page at bare "/", which is what every app relaunch loads, must open
// home at the persisted root view. A boot path that passes a literal 0,0,1 root
// fallback opens home at the origin no matter what the user left.
test('a bare-URL boot restores the persisted home viewport', async ({ gw, window }) => {
  // Reframe home's root grid, the boot anchor, then let the settle persister
  // write the root view.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'reframe changed the zoom').not.toBeCloseTo(1.0, 2);
  await gw.waitIdle();
  // Wait on server truth rather than a sleep: the settle persister's root
  // framing shows up as home's persisted root view.
  await expect
    .poll(
      async () => {
        const pl = (await gw.plugins()).find((x) => x.label === 'home');
        return Number(pl?.rootViewZoom ?? 0);
      },
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);

  // A bare url is the fresh-launch shape: no viewport params to win over the
  // stored root view.
  await window.evaluate(() => {
    globalThis.location.href = '/?e2e=1';
  });
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await window.waitForFunction(
    () => {
      try {
        return ((window as any).__gridwellTest.panes() as Array<{ focused: boolean; anchor: string }>).some(
          (p) => p.focused && p.anchor !== '',
        );
      } catch {
        return false;
      }
    },
    null,
    { timeout: 30_000 },
  );
  // Poll: a slow boot applies the viewport late, after the fetch and handshake.
  await expect
    .poll(async () => (await gw.focused()).zoom, { timeout: 10_000 })
    .toBeCloseTo(left.zoom, 1);
  const back = await gw.focused();
  // Root-view origins are stored as integer cells, so the center round-trips
  // within half a cell. That is the storage resolution, not a framing loss.
  expect(Math.abs(back.cx - left.cx), 'boot restored the persisted center x').toBeLessThan(0.51);
  expect(Math.abs(back.cy - left.cy), 'boot restored the persisted center y').toBeLessThan(0.51);
});

// Ascending after a reload, where the restored frames carry no viewport, must
// land on the parent's persisted framing, what a fresh descent into it would
// show, not an arbitrary zoom-1 origin.
test('a post-reload ascent restores the parent framing it was left at', async ({ gw, window }) => {
  // Home, the boot anchor, is itself a root grid. Reframe that root first, since
  // it is the framing the ascent must come back to, then descend into a well and
  // reload.
  await gw.wheelAtFocusedCenter(-240);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy));
  const parentLeft = await gw.focused();
  const cx = Math.round(parentLeft.cx);
  const cy = Math.round(parentLeft.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  // Server truth before reloading: the root view landed. The descent flush posts
  // it and the handshake mirrors it.
  await expect
    .poll(
      async () => {
        const pl = (await gw.plugins()).find((x) => x.label === 'home');
        return Number(pl?.rootViewZoom ?? 0);
      },
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);

  // The url writer is debounced, so reload only once the descent path is in the
  // url; otherwise the reload lands at the root with nothing to ascend from.
  await expect
    .poll(() => window.evaluate(() => globalThis.location.pathname), { timeout: 10_000 })
    .not.toBe('/');

  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await window.waitForFunction(
    () => {
      try {
        return ((window as any).__gridwellTest.panes() as Array<{ focused: boolean; textFocus: string; path: string[] }>).some(
          (p) => p.focused,
        );
      } catch {
        return false;
      }
    },
    null,
    { timeout: 30_000 },
  );
  await gw.waitIdle();
  expect((await gw.focused()).path.length, 'the reload restored the descent').toBe(1);

  await gw.ascendViaCrumb();
  await expect
    .poll(async () => (await gw.focused()).zoom, { timeout: 10_000 })
    .toBeCloseTo(parentLeft.zoom, 1);
  const back = await gw.focused();
  expect(back.zoom, 'the parent zoom is the persisted one, not 1.0').toBeCloseTo(parentLeft.zoom, 1);
  // Same half-cell storage resolution as the boot spec above.
  expect(Math.abs(back.cx - parentLeft.cx), 'the parent center x came back').toBeLessThan(0.51);
  expect(Math.abs(back.cy - parentLeft.cy), 'the parent center y came back').toBeLessThan(0.51);
});
