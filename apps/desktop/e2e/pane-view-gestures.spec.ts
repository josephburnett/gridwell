import { test, expect } from './fixtures';

// Drives the pane-management and view-framing gestures. These have no server
// footprint (they reframe/relayout the client), so the oracle here is the
// read-only panes() hook: pane count for split, viewport center for pan, zoom
// for wheel, and the framed grid id / path for ascend.

test('wheel zoom and drag-pan reframe the focused pane', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const before = await gw.focused();

  // Wheel up (negative dy) zooms in: the focused pane's zoom changes.
  await gw.wheelAtFocusedCenter(-300);
  const zoomed = await gw.focused();
  expect(zoomed.zoom, 'wheel changed the zoom').not.toBeCloseTo(before.zoom, 3);

  // Drag-pan from one empty cell to another shifts the viewport center. The
  // root grid is empty after entry, so the press lands on no tile → it pans.
  const cx = Math.round(zoomed.cx);
  const cy = Math.round(zoomed.cy);
  await gw.panFocusedGrid(cx, cy, cx - 1, cy - 1);
  const panned = await gw.focused();
  const moved = Math.abs(panned.cx - zoomed.cx) + Math.abs(panned.cy - zoomed.cy);
  expect(moved, 'drag-pan shifted the viewport center').toBeGreaterThan(0.1);
});

test('right-drag splits the focused pane into two — another view of the same grid', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const before = await gw.focused();
  expect((await gw.panes()).length, 'starts with one pane').toBe(1);

  await gw.splitFocusedPaneVertical();

  const panes = await gw.panes();
  expect(panes.length, 'split produced a second pane').toBe(2);
  // "Split" means "another view of where I am" (issue #27): the new pane
  // clones the source's grid — same anchor, same path — not the landing page.
  const other = panes.find((p) => p.id !== before.id)!;
  expect(other.gridID, 'the new pane shows the SAME grid').toBe(before.gridID);
  expect(other.anchor).toBe(before.anchor);
});

// Issue #203: one behavior per button on a border. LEFT-drag owns the whole
// resize job — including CLOSING a side dragged all the way across to the
// corridor's edge at release (issue #204: the minimum wall is a resize
// clamp, not a close threshold). RIGHT-drag from a border is the SPLIT
// gesture (a new pane), exactly like right-drag from inside the pane; short
// of the minimum it cancels silently.
test('left-drag resizes the divider both ways; dragging to the edge closes the side', async ({
  gw,
}) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // Left-drag left → the left pane shrinks; right → grows (clamped resize).
  const r = await gw.resizeDivider('left', -150);
  expect(r.after, 'left-drag shrank the left pane').toBeLessThan(r.before);
  const l = await gw.resizeDivider('left', 150);
  expect(l.after, 'left-drag grew the left pane').toBeGreaterThan(l.before);

  // Drag ALL THE WAY across the left pane — into the close band at the
  // corridor's edge (within gesture.CloseBandPx = 8) — and release: it
  // closes. The target stays INSIDE the viewport (4px from the pane's left
  // edge) — CDP does not deliver events at off-viewport coordinates.
  const ps = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const g = await gw.resizeDivider('left', -(ps[0].w - 4));
  expect(g.after, 'the side dragged to the edge collapsed at release').toBe(0);
  expect((await gw.panes()).length, 'one pane remains').toBe(1);
});

test('right-drag from the divider splits (a new pane); short of the minimum it cancels', async ({
  gw,
}) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // A short right-drag from the divider (under the minimum pane size)
  // cancels silently — no new pane, and no resize either.
  await gw.resizeDivider('right', -10);
  expect((await gw.panes()).length, 'short drag cancels').toBe(2);

  // A right-drag well past the minimum creates a pane — the same split
  // gesture as from inside the pane.
  await gw.resizeDivider('right', -200);
  expect((await gw.panes()).length, 'border right-drag split a pane').toBe(3);
});

test('right-click on the corner circle ascends out of a descended well', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const root = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  expect((await gw.focused()).gridID, 'descended').not.toBe(root);

  await gw.rightClickPlus();
  expect((await gw.focused()).gridID, 'corner right-click ascended to root').toBe(root);
});

test('middle-click ascends out of a descended well', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const root = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  // Create a well and descend into it.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();
  expect(inside.gridID, 'descended into the well child grid').not.toBe(root);

  // Middle-click ascends back to the parent grid (the universal ascend).
  await gw.middleClickCell(cx, cy);
  const out = await gw.focused();
  expect(out.gridID, 'middle-click returned to the root grid').toBe(root);
});

// Focus-first click (issue #28): clicking a pane that was NOT focused at
// press time only moves focus — even when the click lands on a tile. Acting
// (descent) requires the pane to have been focused before the press, the
// same rule the + button and corner circle follow. Without this, "click the
// other pane to focus it" could descend into whatever tile sat under the
// cursor — the old launcher ambiguity, generalized and closed.
test('clicking an unfocused pane focuses without descending; the second click descends', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);

  // Pane B: split (lands on the node grid), focus it by corner click.
  await gw.splitFocusedPaneVertical();
  const b = (await gw.panes()).find((p) => p.id !== a.id)!;
  await gw.clickScreen(b.x + 20, b.y + 20);
  expect((await gw.panes()).find((p) => p.id === b.id)!.focused, 'pane B focused').toBe(true);

  // First click on pane A's TILE: focus moves, no descent.
  const c = await gw.cellCenter(a.id, cx, cy);
  await gw.clickScreen(c.x, c.y);
  let aNow = (await gw.panes()).find((p) => p.id === a.id)!;
  expect(aNow.focused, 'first click focused pane A').toBe(true);
  expect(aNow.textFocus, 'first click did NOT descend into the tile').toBe('');

  // Second click (pane now focused): descends into the markdown tile.
  await gw.clickScreen(c.x, c.y);
  await gw.waitIdle();
  aNow = (await gw.panes()).find((p) => p.id === a.id)!;
  expect(aNow.textFocus, 'second click descended').not.toBe('');
});

// Ascent-history hygiene (issue #26): the two stacks have DISJOINT owners —
// pane.Up frames own portal crossings, the session stack owns in-namespace
// well descents. A balanced excursion of BOTH kinds must return both depths
// to zero: a portal descent that also pushed the session stack would leak an
// orphan there, which a boot-descended pane could later mis-consume as a
// well-ascent viewport. The portal here is a + menu descent into a SECOND
// plugin (boot already sits inside the first, frameless).
test.describe('stack hygiene', () => {
  test.use({ extraPlugins: [{ kind: 'localdb', name: 'second' }] });

  test('portal and well round trips leave both ascent stacks empty', async ({ gw }) => {
    const home = (await gw.focused()).anchor;
    await gw.enterPlugin('second'); // + menu portal descent (frame pushed)
    const f = await gw.focused();
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy) - 1;
    await gw.openPalette();
    await gw.dragCreate('well', cx, cy);

    await gw.descendCell(cx, cy); // well descent (session stack pushed)
    let cur = (await gw.panes()).find((p) => p.id === f.id)!;
    expect(cur.frameDepth, 'one portal frame while inside the plugin').toBe(1);
    expect(cur.ascentDepth, 'one well level saved').toBe(1);

    // Pane-center clicks: the ascend is position-independent, and a
    // computed cell center can be OFF-PANE at the child grid's high zoom
    // (the silent no-op behind this spec's long flake history, #195).
    await gw.middleClickPane(); // well ascent
    await gw.middleClickPane(); // portal ascent back home

    cur = (await gw.panes()).find((p) => p.id === f.id)!;
    expect(cur.anchor, 'back home (the boot anchor)').toBe(home);
    expect(cur.frameDepth, 'no frame leaked').toBe(0);
    expect(cur.ascentDepth, 'no session-stack entry leaked (the orphan bug)').toBe(0);
  });
});
