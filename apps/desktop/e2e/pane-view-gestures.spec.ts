import { test, expect } from './fixtures';

// Drives the pane-management and view-framing gestures. They reframe and
// relayout the client and leave no server footprint, so the oracle is the
// read-only panes() hook: pane count for a split, viewport center for a pan,
// zoom for the wheel, and the framed grid id and path for an ascend.

test('wheel zoom and drag-pan reframe the focused pane', async ({ gw }) => {
  await gw.enterPlugin('home');
  const before = await gw.focused();

  // Wheel up, a negative dy, zooms in: the focused pane's zoom changes.
  await gw.wheelAtFocusedCenter(-300);
  const zoomed = await gw.focused();
  expect(zoomed.zoom, 'wheel changed the zoom').not.toBeCloseTo(before.zoom, 3);

  // Drag-panning from one empty cell to another shifts the viewport center. The
  // root grid is empty after entry, so the press lands on no tile and pans.
  const cx = Math.round(zoomed.cx);
  const cy = Math.round(zoomed.cy);
  await gw.panFocusedGrid(cx, cy, cx - 1, cy - 1);
  const panned = await gw.focused();
  const moved = Math.abs(panned.cx - zoomed.cx) + Math.abs(panned.cy - zoomed.cy);
  expect(moved, 'drag-pan shifted the viewport center').toBeGreaterThan(0.1);
});

test('right-drag splits the focused pane into two — another view of the same grid', async ({ gw }) => {
  await gw.enterPlugin('home');
  const before = await gw.focused();
  expect((await gw.panes()).length, 'starts with one pane').toBe(1);

  await gw.splitFocusedPaneVertical();

  const panes = await gw.panes();
  expect(panes.length, 'split produced a second pane').toBe(2);
  // A split is another view of where you are, so the new pane clones the
  // source's grid, anchor and path.
  const other = panes.find((p) => p.id !== before.id)!;
  expect(other.gridID, 'the new pane shows the SAME grid').toBe(before.gridID);
  expect(other.anchor).toBe(before.anchor);
});

// One behavior per button on a border. The left drag owns the whole resize job,
// including closing a side that is crushed past its minimum and released there.
// A right drag from a border is the split gesture, the same as a right drag
// from inside the pane, and short of the minimum it cancels silently.
test('left-drag resizes the divider both ways; dragging to the edge closes the side', async ({
  gw,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // Dragging left shrinks the left pane and dragging right grows it, clamped.
  const r = await gw.resizeDivider('left', -150);
  expect(r.after, 'left-drag shrank the left pane').toBeLessThan(r.before);
  const l = await gw.resizeDivider('left', 150);
  expect(l.after, 'left-drag grew the left pane').toBeGreaterThan(l.before);

  // Drag all the way across the left pane, crushing it past its minimum
  // (pane.CrushPlan), and release: it closes. The target stays inside the
  // viewport, 4px from the pane's left edge, because CDP does not deliver
  // events at off-viewport coordinates.
  const ps = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const g = await gw.resizeDivider('left', -(ps[0].w - 4));
  expect(g.after, 'the side dragged to the edge collapsed at release').toBe(0);
  expect((await gw.panes()).length, 'one pane remains').toBe(1);
});

test('right-drag from the divider splits (a new pane); short of the minimum it cancels', async ({
  gw,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length).toBe(2);

  // A short right drag from the divider, under the minimum pane size, cancels
  // silently, with no new pane and no resize either.
  await gw.resizeDivider('right', -10);
  expect((await gw.panes()).length, 'short drag cancels').toBe(2);

  // A right drag well past the minimum creates a pane, the same split gesture
  // as from inside the pane.
  await gw.resizeDivider('right', -200);
  expect((await gw.panes()).length, 'border right-drag split a pane').toBe(3);
});

// The split's side follows the drag rather than the grab. One border press can
// travel one way, cross back, and commit on the other side, and the new pane
// opens in whichever pane the cursor released in.
test('a border right-drag flips direction mid-gesture and splits where it releases', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(2);
  const [left, right] = before;

  // Press on the shared border, drag left into the left pane, then cross back
  // and release inside the right pane. The new pane opens between the border
  // and the release point, in the right pane's territory.
  const gx = left.x + left.w;
  const gy = left.y + left.h / 2;
  await window.mouse.move(gx - 2, gy);
  await window.mouse.down({ button: 'right' });
  await window.mouse.move(gx - 120, gy, { steps: 6 });
  await window.mouse.move(gx + 150, gy, { steps: 8 });
  await window.mouse.up({ button: 'right' });
  await gw.waitIdle();

  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after, 'the flipped drag still split').toHaveLength(3);
  // The middle pane of the three starts at the old border.
  expect(Math.abs(after[1].x - gx)).toBeLessThan(12);
  expect(after[1].w, 'the new pane spans border→release').toBeLessThan(right.w / 2 + 40);
});

test('crumb click ascends; a right-click on the slot no longer does (#222)', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const root = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  expect((await gw.focused()).gridID, 'descended').not.toBe(root);

  // Right-clicking the slot must do nothing.
  const pal = await gw.palette();
  await window.mouse.click(pal.plusX, pal.plusY, { button: 'right' });
  await gw.waitIdle();
  expect((await gw.focused()).gridID, 'slot right-click must not ascend').not.toBe(root);

  // The ascent gesture: click the previous crumb.
  await gw.ascendViaCrumb();
  expect((await gw.focused()).gridID, 'crumb click ascended to root').toBe(root);
});

test('middle-click ascends out of a descended well', async ({ gw }) => {
  await gw.enterPlugin('home');
  const root = (await gw.focused()).gridID;
  const cx = Math.round((await gw.focused()).cx);
  const cy = Math.round((await gw.focused()).cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();
  expect(inside.gridID, 'descended into the well child grid').not.toBe(root);

  // A middle-click is the in-pane ascend, whatever the pane is showing.
  await gw.middleClickCell(cx, cy);
  const out = await gw.focused();
  expect(out.gridID, 'middle-click returned to the root grid').toBe(root);
});

// A click lands on what it hits, in any pane, and pane focus follows in the
// same press: one click on a tile in an unfocused pane descends into it, and
// with ctrl held it descends in a split below.
test('one click on a tile in an unfocused pane focuses it and descends', async ({ gw }) => {
  await gw.enterPlugin('home');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);

  await gw.splitFocusedPaneVertical();
  const b = (await gw.panes()).find((p) => p.id !== a.id)!;
  await gw.clickScreen(b.x + 20, b.y + 20);
  expect((await gw.panes()).find((p) => p.id === b.id)!.focused, 'pane B focused').toBe(true);

  const c = await gw.cellCenter(a.id, cx, cy);
  await gw.clickScreen(c.x, c.y);
  const aNow = (await gw.panes()).find((p) => p.id === a.id)!;
  expect(aNow.focused, 'the click focused pane A').toBe(true);
  expect(aNow.textFocus, 'the same click descended into the tile').not.toBe('');
});

test('one ctrl+click on a tile in an unfocused pane descends in a split below', async ({ gw }) => {
  await gw.enterPlugin('home');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);

  await gw.splitFocusedPaneVertical();
  const b = (await gw.panes()).find((p) => p.id !== a.id)!;
  await gw.clickScreen(b.x + 20, b.y + 20);
  const before = (await gw.panes()).length;

  await gw.ctrlDescendCell(cx, cy, a.id);
  const after = await gw.panes();
  expect(after.length, 'the ctrl+click opened a split').toBe(before + 1);
  const opened = after.find((p) => p.focused)!;
  expect(opened.textFocus, 'the split descended into the tile').not.toBe('');
  expect(after.find((p) => p.id === a.id)!.textFocus, 'pane A kept its grid').toBe('');
});

// A balanced excursion, a namespace crossing plus a well descent, must return
// the pane's place to depth zero. There is one place stack, so one number
// answers it. Two stacks with disjoint owners would let a descent push the wrong
// one and leak an orphan a later ascent could mis-consume as a viewport. The
// crossing here is a + menu descent into a second namespace, since boot already
// sits at depth 0 in home.
test.describe('stack hygiene', () => {
  test.use({ extraNodes: ['second'] });

  test('a namespace crossing and a well round trip leave the place stack empty', async ({ gw }) => {
    const home = (await gw.focused()).anchor;
    await gw.enterPlugin('second'); // the + menu crossing pushes one frame
    const f = await gw.focused();
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy) - 1;
    await gw.openPalette();
    await gw.dragCreate('well', cx, cy);

    await gw.descendCell(cx, cy); // the well descent pushes a second frame
    let cur = (await gw.panes()).find((p) => p.id === f.id)!;
    expect(cur.placeDepth, 'two doorways deep: a well, then the plugin link').toBe(2);

    // Pane-center clicks: the ascend is position-independent, and a computed
    // cell center can land off-pane at the child grid's high zoom, which is the
    // silent no-op behind this spec's flake history.
    await gw.middleClickPane(); // pop the well frame
    await gw.middleClickPane(); // pop the crossing, back home

    cur = (await gw.panes()).find((p) => p.id === f.id)!;
    expect(cur.anchor, 'back home (the boot anchor)').toBe(home);
    expect(cur.placeDepth, 'no frame leaked (the orphan bug)').toBe(0);
  });
});
