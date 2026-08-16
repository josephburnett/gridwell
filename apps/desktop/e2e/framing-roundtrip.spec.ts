import { test, expect } from './fixtures';

// Locks the descend → reframe → ascend round trip: invariant I7, "preview =
// descent target = ascent return" (CLAUDE.md face #3; ARCHITECTURE.md §11). This
// is the invariant the owner's "previews go wonky" reports are about, and the
// test forensics found it had no test home at all — the framing round trip was
// asserted nowhere. It is also the safety net any framing refactor (Phase 1b)
// must have first: these tests must stay green while the framing copies are given
// a single owner.
//
// Both tests drive the real app and read framing from the panes() hook (the live
// viewport the user sees), so they catch a desync between the live pane, the
// saved ascent state, and the server-persisted well view_*.
//
// The plugin-root viewport test (I7-portal) locks invariant I7 at the
// menu↔plugin-root seam: enter a plugin from the + menu, pan/zoom its root
// grid, ascend, re-enter — the viewport must be exactly as left. The seam
// moved when the launcher landing page was reversed (2026-07-19): a menu
// portal has no containing link tile, so the ascent writeback goes straight
// to the plugin's root view (SetRootView) instead of through a node-grid
// tile. This seam was broken once before (every re-entry reset to the
// default calibrated zoom), so it keeps its crossing test.

test.use({ extraPlugins: [{ kind: 'local', name: 'second' }] });

test('re-descending a reframed well returns to exactly what you left', async ({ gw }) => {
  await gw.enterPlugin('local');
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

  // Ascend out, then re-descend: must land on exactly the framing we left.
  await gw.middleClickCell(Math.round(left.cx), Math.round(left.cy));
  expect((await gw.focused()).gridID, 'ascended out of the child').not.toBe(childGrid);

  await gw.descendCell(cx, cy);
  const back = await gw.focused();
  expect(back.gridID, 're-descended into the same child grid').toBe(childGrid);
  expect(back.zoom, 'zoom round-tripped').toBeCloseTo(left.zoom, 1);
  expect(back.cx, 'center x round-tripped').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y round-tripped').toBeCloseTo(left.cy, 1);
});

test('plugin root-grid viewport persists across + menu ascent and re-entry', async ({ gw, window }) => {
  // Invariant: enter a plugin from the + menu, reframe its root grid, ascend
  // (portal back home), re-enter — viewport must match what was left
  // (issue #32, rehomed onto the menu portal 2026-07-19).
  await gw.enterPlugin('second');
  const pluginGrid = (await gw.focused()).gridID;

  // Reframe the plugin root: zoom in, then pan. The grid is empty so the
  // press lands on nothing and pans.
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

  // The write is SERVER truth, not just a client cache: the node grid serves
  // each plugin's root view as its link tile's framing, so the reframed zoom
  // must show up there. Poll — the SetRootView post is async.
  const nodeGrid = await window.evaluate(() => (window as any).__gridwellTest.nodeGrid());
  await expect
    .poll(
      async () => {
        const ng = await gw.getGrid(nodeGrid);
        const t = (ng.tiles ?? []).find((x: { altText?: string }) => x.altText === 'second');
        return Number((t as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { timeout: 5_000 },
    )
    .toBeGreaterThan(0);

  // Re-enter from the menu: viewport must match what we left.
  await gw.enterPlugin('second');
  const back = await gw.focused();
  expect(back.gridID, 're-entered the same plugin root').toBe(pluginGrid);
  expect(back.zoom, 'zoom restored after re-entry').toBeCloseTo(left.zoom, 1);
  expect(back.cx, 'center x restored after re-entry').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y restored after re-entry').toBeCloseTo(left.cy, 1);
});

test('a reframe persists without ascending (issue #190)', async ({ gw }) => {
  // Before the settle persister, the ONLY framing writers were the ascent
  // flushes — leave a grid any other way (reload, pane switch, URL edit,
  // descend deeper) and the viewport silently reverted. This test pins the
  // fix at the real seam: reframe a child grid and, WITHOUT ascending, the
  // well's view_* must show up in server truth on its own.
  await gw.enterPlugin('local');
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

  // No ascent, no navigation: the debounced settle persister alone must
  // write the framing. A fresh well's viewZoom is 0 until the first write.
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

test('a plugin root reframe persists without ascending (issue #190)', async ({ gw, window }) => {
  // Same invariant one seam over: pan/zoom a plugin's ROOT grid and the
  // root view must reach the server without a + menu ascent (SetRootView
  // used to fire only from the portal-ascent path).
  await gw.enterPlugin('second');

  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'reframe actually changed the zoom').not.toBeCloseTo(1.0, 2);

  // The node grid serves each plugin's root view as its link tile's framing
  // — poll it WITHOUT ascending.
  const nodeGrid = await window.evaluate(() => (window as any).__gridwellTest.nodeGrid());
  await expect
    .poll(
      async () => {
        const ng = await gw.getGrid(nodeGrid);
        const t = (ng.tiles ?? []).find((x: { altText?: string }) => x.altText === 'second');
        return Number((t as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { timeout: 5_000 },
    )
    .toBeGreaterThan(0);
});

test('ascending restores the parent viewport unchanged', async ({ gw }) => {
  await gw.enterPlugin('local');
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

// A fresh page at bare "/" (what every app relaunch loads) must open home
// at the PERSISTED root view — the boot path passed literal 0,0,1 for the
// root fallback since the parameter existed, so every relaunch opened
// home at the origin no matter what the user left (2026-08-13).
test('a bare-URL boot restores the persisted home viewport', async ({ gw, window }) => {
  // Reframe HOME's root grid (the boot anchor), then let the settle
  // persister write the root view.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  expect(left.zoom, 'reframe changed the zoom').not.toBeCloseTo(1.0, 2);
  await gw.waitIdle();
  // Wait on SERVER truth, not a sleep: the settle persister's SetRootView
  // shows up as the home plugin's node-grid link framing.
  const nodeGrid1 = await window.evaluate(() => (window as any).__gridwellTest.nodeGrid());
  await expect
    .poll(
      async () => {
        const ng = await gw.getGrid(nodeGrid1);
        const t = (ng.tiles ?? []).find((x: { altText?: string }) => x.altText === 'e2e');
        return Number((t as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);

  // A bare URL is the fresh-launch shape: no viewport params to win over
  // the stored root view.
  await window.evaluate(() => {
    window.location.href = '/?e2e=1';
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
  // Poll: a slow boot applies the viewport late (fetch + handshake).
  await expect
    .poll(async () => (await gw.focused()).zoom, { timeout: 10_000 })
    .toBeCloseTo(left.zoom, 1);
  const back = await gw.focused();
  // Root-view origins are stored as INTEGER cells (the schema is additive-
  // only), so the center round-trips within half a cell — the storage
  // resolution, not a framing loss.
  expect(Math.abs(back.cx - left.cx), 'boot restored the persisted center x').toBeLessThan(0.51);
  expect(Math.abs(back.cy - left.cy), 'boot restored the persisted center y').toBeLessThan(0.51);
});

// Ascending after a RELOAD (empty session ascent stack) must land on the
// parent's persisted framing — what a fresh descent into it would show —
// not an arbitrary zoom-1 origin (2026-08-13).
test('a post-reload ascent restores the parent framing it was left at', async ({ gw, window }) => {
  // Home (the boot anchor) IS a plugin root — reframe the plugin ROOT first (this is the framing the ascent must
  // come back to), then descend into a well and reload.
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
  // Server truth before reloading: the root view landed (the descent
  // flush posts it; the node grid mirrors it).
  const nodeGrid2 = await window.evaluate(() => (window as any).__gridwellTest.nodeGrid());
  await expect
    .poll(
      async () => {
        const ng = await gw.getGrid(nodeGrid2);
        const t = (ng.tiles ?? []).find((x: { altText?: string }) => x.altText === 'e2e');
        return Number((t as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);

  // The URL writer is debounced: reload only after the descent path is
  // actually IN the URL, or the reload lands at the root with nothing to
  // ascend from.
  await expect
    .poll(() => window.evaluate(() => window.location.pathname), { timeout: 10_000 })
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
