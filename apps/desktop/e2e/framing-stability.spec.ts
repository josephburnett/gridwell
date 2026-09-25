import { test, expect } from './fixtures';
import { tileAt, placeTile } from './oracle';
import { settle } from './cadence';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// Framing saves survive races, unloads, and sibling panes, and every root grid
// persists its viewport.

const FS_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-framing-'));
// A second fs root, with a document and a subdirectory in it: the read-only
// scroll test needs a file to descend into and the mid-descent reframe needs a
// doorway. FS_ROOT stays empty for the root-grid pan test, whose press would
// otherwise land on a tile instead of the grid.
const DOC_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-framing-doc-'));
fs.writeFileSync(path.join(DOC_ROOT, 'long.md'), '# long\n\n' + 'line\n\n'.repeat(200));
fs.mkdirSync(path.join(DOC_ROOT, 'papers'));
fs.writeFileSync(path.join(DOC_ROOT, 'papers', 'one.md'), '# one\n');
// Two plugins go through the FIXTURE form. Playwright reads a two-element array
// option as its [value, options] tuple, so a literal pair would silently seed
// only the first, as a home with no plugins at all.
test.use({
  extraPlugins: async ({}, use) => {
    await use([
      { kind: 'fs', name: 'pics', config: { root: FS_ROOT } },
      { kind: 'fs', name: 'docs', config: { root: DOC_ROOT } },
    ]);
  },
});

// A framing save that races a version-bumping write retries with a fresh claim
// instead of dropping silently.
test('a framing save survives a racing version bump', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.wheelAtFocusedCenter(-240); // reframe the child
  // Race: a foreign placement bumps the well's version inside the settle window,
  // so the framing flush's claim is stale.
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  await placeTile(gw.origin, well.id, well.version as number, home.gridID, cx, cy, 2, 2);
  await expect
    .poll(
      async () =>
        Number(
          (tileAt(await gw.getGrid(home.gridID), 'well', cx, cy) as { viewZoom?: number | string })
            ?.viewZoom ?? 0,
        ),
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);
  void window;
});

// An fs plugin root persists its viewport; the root framing write must not be
// swallowed on the plugin side.
test('an fs root grid keeps its viewport across leave and re-entry', async ({ gw }) => {
  await gw.enterPlugin('pics');
  const grid = (await gw.focused()).gridID;
  await gw.wheelAtFocusedCenter(-240);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy));
  const left = await gw.focused();
  await gw.ascendViaCrumb();
  await gw.enterPlugin('pics');
  const back = await gw.focused();
  expect(back.gridID).toBe(grid);
  expect(back.zoom, 'fs root zoom restored').toBeCloseTo(left.zoom, 1);
  expect(Math.abs(back.cx - left.cx), 'fs root cx restored').toBeLessThan(0.51);
});

// One pan, one write. Every frame a drag draws arms the framing persister, so a
// window fixed at the first of those arms writes the middle of the gesture
// instead of its end: a 21-minute trace held four SetFraming writes on one grid
// inside two seconds of a single pan, and each of those is a store write, an
// event back to this client, and a repaint. What the user did was reframe a
// grid, once.
test('a pan longer than the settle window writes only where it came to rest', async ({
  gw,
  window,
}) => {
  const c = await gw.cadences();
  await gw.enterPlugin('pics');
  const p = await gw.focused();
  const writes = () =>
    window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetFraming ?? 0));
  // Nothing may be pending when the gesture starts, or the count would carry a
  // window the spec never watched.
  await settle(window, c.framingSaveMs);
  const before = await writes();

  const x = p.x + p.w / 2;
  const y = p.y + p.h / 2;
  await window.mouse.move(x, y);
  await window.mouse.down();
  // Three windows of drag, moving the whole way through: a throttle writes
  // once per window crossed and a settle has nothing to say until the button
  // comes up.
  const steps = 24;
  for (let i = 1; i <= steps; i++) {
    await window.mouse.move(x + i * 3, y + i * 2);
    await window.waitForTimeout((c.framingSaveMs * 3) / steps);
  }
  await window.mouse.up();
  await gw.waitIdle();
  await settle(window, c.framingSaveMs);

  const rested = await gw.focused();
  expect(Math.abs(rested.cx - p.cx), 'the pan moved the view').toBeGreaterThan(0.1);
  expect(await writes(), 'the pan was persisted before the user let go of it').toBe(before + 1);
});

// A reload fired inside the settle window still lands the save: the unload flush
// rides beacons that outlive the page.
test('a reload inside the settle window does not lose the framing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.wheelAtFocusedCenter(-240);
  // No settle wait: reload immediately. The beacon must land server-side.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect
    .poll(
      async () =>
        Number(
          (tileAt(await gw.getGrid(home.gridID), 'well', cx, cy) as { viewZoom?: number | string })
            ?.viewZoom ?? 0,
        ),
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);
});

// Text scroll persists on the settle tick, with no ascent.
test('text scroll persists without an ascent', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  const { updateText } = await import('./oracle');
  await updateText(gw.origin, doc.id, Number(doc.version ?? 0), '# long\n\n' + 'line\n\n'.repeat(200));
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  // Rendered mode: the wheel scrolls the document.
  await window.mouse.move(home.x + home.w / 2, home.y + home.h / 2);
  for (let i = 0; i < 8; i++) await window.mouse.wheel(0, 120);
  await expect
    .poll(
      async () =>
        Number(
          (tileAt(await gw.getGrid(home.gridID), 'text', cx, cy) as { textY?: number | string })
            ?.textY ?? 0,
        ),
      { message: 'the scroll reached server truth with NO ascent', timeout: 10_000 },
    )
    .toBeGreaterThan(0);
});

// A read-only host file scrolls like any other text tile. The body is the
// plugin's; where the user left the window is the node's, held in the plugin's
// namespace of the store (#236, #270).
//
// The scroll is also this entry's FIRST durable fact, so it mints the entry's
// row while the reader is standing on the entry's id. The pane's content id must
// not move under them (#297): a rename would hide the rendered overlay mid-read
// and leave the URL naming a tile the next listing does not contain, so the
// reload would land at the plugin root.
test('a read-only file keeps its scroll, and its id, across a reload', async ({ gw, window }) => {
  await gw.enterPlugin('docs');
  const root = (await gw.focused()).gridID;
  const at = async () => (await gw.getGrid(root)).tiles!.find((t) => t.altText === 'long.md')!;
  const doc = await at();
  expect(doc, 'long.md listed').toBeTruthy();
  await gw.descendCell(Number(doc.x ?? 0), Number(doc.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  const standingOn = (await gw.focused()).textFocus;
  // A read-only tile always shows the rendered face, a scrolling DOM overlay.
  // Scroll it the way the browser does, and the app's own listener writes the
  // position onto the pane.
  await expect
    .poll(() => window.evaluate(() => document.getElementById('gw-rendered-view')?.textContent ?? ''))
    .toContain('line');
  await expect
    .poll(() =>
      window.evaluate(() => {
        const el = document.getElementById('gw-rendered-view')!;
        el.scrollTop = 400;
        el.dispatchEvent(new Event('scroll'));
        return el.scrollTop;
      }),
    )
    .toBeGreaterThan(0);
  // The write that mints. The document must still be on screen after it, under
  // the same id.
  await expect
    .poll(async () => Number(((await at()) as { textY?: number | string })?.textY ?? 0), {
      message: 'a read-only file persists its scroll',
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  expect((await at()).id, 'the listing renamed the entry the pane is showing').toBe(standingOn);
  expect((await gw.focused()).textFocus, 'the mint moved the id under the reader').toBe(standingOn);
  expect(
    await window.evaluate(() => document.getElementById('gw-rendered-view')?.scrollTop ?? -1),
    'the document left the screen when its row was minted',
  ).toBeGreaterThan(0);

  // The URL still names it, so a reload restores the reader inside the file at
  // the scroll they left, with no second descent.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect
    .poll(async () => (await gw.focused()).textFocus, {
      message: 'the reload did not restore the descent into the file',
      timeout: 30_000,
    })
    .toBe(standingOn);
  await expect
    .poll(() => window.evaluate(() => document.getElementById('gw-rendered-view')?.scrollTop ?? 0), {
      message: 'the restore lands on the scroll the user left',
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
});

// A directory doorway reframed MID-DESCENT is that entry's first durable fact,
// and the URL is carrying the doorway's id while it happens. If the mint renamed
// the doorway, the refetched listing would not hold the id the URL names,
// urlwalk.Walk would skip it, and the reload would drop the user at the plugin
// root. #297
test('a directory reframed mid-descent is still there after a reload', async ({ gw, window }) => {
  await gw.enterPlugin('docs');
  const root = (await gw.focused()).gridID;
  const dir = (await gw.getGrid(root)).tiles!.find((t) => t.altText === 'papers');
  expect(dir, 'papers listed').toBeTruthy();
  const doorway = dir!.id;
  await gw.descendCell(Number(dir!.x ?? 0), Number(dir!.y ?? 0));
  await expect.poll(async () => (await gw.focused()).gridID).not.toBe(root);
  const inside = (await gw.focused()).gridID;
  // The reframe: the descent's own zoom, which is what mints the doorway's row.
  await gw.wheelAtFocusedCenter(-240);
  const papers = async () => (await gw.getGrid(root)).tiles!.find((t) => t.altText === 'papers')!;
  await expect
    .poll(async () => Number(((await papers()) as { viewZoom?: number | string }).viewZoom ?? 0), {
      message: 'the framing reached the doorway row',
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  expect((await papers()).id, 'the framing renamed the doorway the URL is carrying').toBe(doorway);

  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect
    .poll(async () => (await gw.focused()).gridID, {
      message: 'the restore landed outside the directory the user was inside',
      timeout: 30_000,
    })
    .toBe(inside);
  expect(
    (await gw.focused()).placeDepth,
    'the restore landed on a root, not through the doorway',
  ).toBeGreaterThan(0);
});

// One active surface per grid: a passive sibling pane never overwrites the
// focused pane's persisted framing.
test('a split sibling never overwrites the focused pane framing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  // Split: two panes now show the same child grid with different rects.
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();
  await gw.wheelAtFocusedCenter(-240);
  const well = () => gw.getGrid(home.gridID).then((g) => tileAt(g, 'well', cx, cy)!);
  await expect
    .poll(async () => Number((await well()).viewZoom ?? 0), { timeout: 10_000 })
    .toBeGreaterThan(0.125);
  const settled = await well();
  // Provoke more settle ticks, through draws, without touching either viewport:
  // the passive sibling must not write its own rect-derived framing back.
  for (let i = 0; i < 3; i++) {
    await window.mouse.move(100 + i, 100);
    await window.waitForTimeout(800);
  }
  const after = await well();
  expect(after.viewZoom, 'sibling must not overwrite (one active surface)').toEqual(settled.viewZoom);
  expect(after.viewCx, 'center stable').toEqual(settled.viewCx);
  expect(after.viewCy, 'center stable').toEqual(settled.viewCy);
});

// A viewport in mid-flight is presentation and never the user's framing. The
// settle persister fires on its own 600ms clock, so a stretched transition puts
// a debounce tick inside the animation, when the pane's centre and zoom are
// racing toward the doorway's overtake and are values the user never chose.
// Only persistFraming decides what a framing write carries, so every writer
// gets the same answer.
test('a mid-flight viewport never becomes the framing that persists', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);

  // Settle a root framing the user chose, so there is something to keep.
  await gw.wheelAtFocusedCenter(-240);
  const uuid = (await gw.plugins()).find((p) => p.kind === 'home')!.uuid;
  const rootView = async () => {
    const pl = (await gw.plugins()).find((p) => p.uuid === uuid)!;
    return { cx: pl.rootViewCx, cy: pl.rootViewCy, zoom: pl.rootViewZoom };
  };
  await expect.poll(async () => (await rootView()).zoom, { timeout: 10_000 }).toBeGreaterThan(0);
  const settled = await rootView();

  // Stretch the clock and descend without waiting: for the next few seconds
  // the pane's zoom climbs toward the well's overtake while the settle
  // persister keeps firing.
  await window.evaluate(`(window).__gridwellTest.setTransitionMs(4000)`);
  const c = await gw.cellCenter(home.id, cx, cy);
  await window.mouse.click(c.x, c.y);
  await expect
    .poll(() => window.evaluate(`(window).__gridwellTest.transitioning()`), { timeout: 10_000 })
    .toBe(true);
  await window.waitForTimeout(1500); // two debounce windows

  expect(
    await window.evaluate(`(window).__gridwellTest.transitioning()`),
    'the descent is still animating, so the read below is genuinely mid-flight',
  ).toBe(true);
  expect(
    await rootView(),
    'a frame of the descent animation became the durable root framing',
  ).toEqual(settled);

  await window.evaluate(`(window).__gridwellTest.setTransitionMs(350)`);
  await gw.waitIdle(30_000);
  expect(await rootView(), 'the landing moved it too').toEqual(settled);
});

// Reading never mutates. A zero row is not a framing but the absence of one,
// and its readers show a fallback in its place, so the settle persister must
// diff against that fallback: against the row itself every grid is stamped on
// the first settle tick after it is merely looked at, and a root grid's stamp
// is derived from the window it was looked at in.
test('looking at a grid never stamps a framing on it', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const uuid = (await gw.plugins()).find((p) => p.kind === 'home')!.uuid;
  // Two settle windows with no gesture in them at all.
  await window.waitForTimeout(1500);
  const pl = (await gw.plugins()).find((p) => p.uuid === uuid)!;
  expect(Number(pl.rootViewZoom ?? 0), 'the root grid was stamped by being looked at').toBe(0);

  // A descent is a look too: the doorway it entered by keeps its zero.
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  await window.waitForTimeout(1500);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(
    Number((well as { viewZoom?: number | string }).viewZoom ?? 0),
    'the doorway was stamped by being descended through',
  ).toBe(0);
});
