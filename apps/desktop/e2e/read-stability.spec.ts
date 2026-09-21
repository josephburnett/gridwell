import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';

// Reading never mutates, at the gesture that reads: a descent shows a thing and
// an ascent puts it back, so a round trip the user made no gesture inside must
// leave the tile exactly as it was, whatever kind it is. Every framing test
// beside this one is positive — move the view, check the move persisted — and a
// persister that writes back the view it was merely shown passes all of them.
//
// Two oracles, because either alone can be satisfied while the other is not.
// persistPosts counts the settle persister's own dispatches, so a write the
// server happens to store as the same bytes is still caught; the tile row is
// the server's answer, so a write that arrived by another path is caught too. A
// capture is deliberately not counted: an ascent out of a live url or shell
// freezes a frame, which is what the machine observed rather than what the user
// left, and it carries no claim and no bump.
//
// The trip under test is not a tile's first, uniformly for all four kinds,
// because a text tile's first look still stamps it: descending into a document
// nobody has opened writes text_w and text_h from the window it was opened in,
// and text_mode from the descent. That is the stamp f1161d94 took off a grid
// row, on the row a text tile keeps its window in.

// framingWrites is the whole of what the settle persister dispatches.
async function framingWrites(window: any): Promise<number> {
  return window.evaluate(() => {
    const p = (window as any).__gridwellTest.persistPosts();
    return Number(p.SetFraming ?? 0) + Number(p.SetTextView ?? 0) + Number(p.SetContentZoom ?? 0);
  });
}

// stable is what a look may not change: where the tile sits, how the grid
// behind it was framed, which window of it was shown, and the version that
// means its bytes.
function stable(t: Record<string, unknown> | undefined): string {
  const n = (k: string) => Number(t?.[k] ?? 0);
  return JSON.stringify({
    x: n('x'),
    y: n('y'),
    w: n('w'),
    h: n('h'),
    viewCx: n('viewCx'),
    viewCy: n('viewCy'),
    viewZoom: n('viewZoom'),
    textX: n('textX'),
    textY: n('textY'),
    textW: n('textW'),
    textH: n('textH'),
    textMode: String(t?.textMode ?? ''),
    contentZoom: n('contentZoom'),
    version: n('version'),
  });
}

// roundTrip is the pure read: in, stay still, out, stay still. Both settles are
// inside it, so the persister has had a window to write in on each side and the
// count is read once it has gone quiet.
async function roundTrip(gw: any, window: any, cx: number, cy: number, settleMs: number) {
  await gw.descendCell(cx, cy);
  await settle(window, settleMs);
  await gw.ascendViaCrumb();
  await gw.waitIdle();
  await settle(window, settleMs);
}

// The four kinds a descent can land on. A well pushes a frame onto its child
// grid; a text, url or shell tile pushes one onto the tile itself, and each
// keeps a different framing fact, so one of them staying still proves nothing
// about the others.
for (const kind of ['well', 'markdown', 'url', 'shell'] as const) {
  test(`looking inside a ${kind} tile and coming back writes nothing`, async ({ gw, window }) => {
    const settleMs = (await gw.cadences()).framingSaveMs;
    await gw.enterPlugin('home');
    const home = await gw.focused();
    const cx = Math.round(home.cx);
    const cy = Math.round(home.cy);

    await gw.openPalette();
    await gw.dragCreate(kind, cx, cy);
    const rowKind = kind === 'markdown' ? 'text' : kind;
    await expect
      .poll(async () => tileAt(await gw.getGrid(home.gridID), rowKind, cx, cy))
      .toBeTruthy();

    if (kind === 'url') {
      // A url tile lands bare; the address is the one thing that must be typed
      // before there is anything to look at.
      await gw.descendCell(cx, cy);
      await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
      await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?read=1`);
      await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
      await gw.ascendViaCrumb();
      await gw.waitIdle();
    }

    // The first visit, whose stamp is the header's subject.
    await roundTrip(gw, window, cx, cy, settleMs);

    const before = tileAt(await gw.getGrid(home.gridID), rowKind, cx, cy)!;
    const writes = await framingWrites(window);

    await roundTrip(gw, window, cx, cy, settleMs);

    const after = tileAt(await gw.getGrid(home.gridID), rowKind, cx, cy)!;
    expect(stable(after), `the ${kind} round trip changed the stored row`).toBe(stable(before));
    expect(await framingWrites(window), `the ${kind} round trip dispatched a framing write`).toBe(
      writes,
    );

    if (kind === 'shell') {
      // tmux outlives the page, so the session goes with the tile.
      await gw.deleteTileCell(cx, cy);
    }
  });
}

// A boot is a read too: the place comes off the URL, the views come off the
// rows, and until the user moves, nothing about any of them has changed. The
// settle persister is armed by the first draw, so a boot-time stamp lands here
// and nowhere else — and a zero proves that only while the persister is
// actually running, which is why framingFlushes is asserted beside it: an
// unarmed debounce takes every count quiet with it.
for (const kind of ['well', 'markdown'] as const) {
  test(`a reload inside a ${kind} writes nothing until the user moves`, async ({ gw, window }) => {
    const settleMs = (await gw.cadences()).framingSaveMs;
    await gw.enterPlugin('home');
    const home = await gw.focused();
    const cx = Math.round(home.cx);
    const cy = Math.round(home.cy);
    const rowKind = kind === 'markdown' ? 'text' : kind;

    await gw.openPalette();
    await gw.dragCreate(kind, cx, cy);
    await expect
      .poll(async () => tileAt(await gw.getGrid(home.gridID), rowKind, cx, cy))
      .toBeTruthy();
    if (kind === 'markdown') {
      // Past the first look's stamp; see the header.
      await roundTrip(gw, window, cx, cy, settleMs);
    }
    await gw.descendCell(cx, cy);
    await settle(window, settleMs);
    const inside = await gw.focused();
    const before = tileAt(await gw.getGrid(home.gridID), rowKind, cx, cy)!;

    await window.reload();
    await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await expect
      .poll(async () => (await gw.focused()).gridID, { timeout: 30_000 })
      .toBe(inside.gridID);
    await gw.waitIdle(30_000);
    await settle(window, settleMs);
    await settle(window, settleMs);

    expect(
      await window.evaluate(() =>
        Number((window as any).__gridwellTest.persistPosts().framingFlushes ?? 0),
      ),
      'the settle persister never ran, so a zero below would mean nothing',
    ).toBeGreaterThan(0);
    expect(await framingWrites(window), `the restored ${kind} was stamped by being restored`).toBe(
      0,
    );
    const after = tileAt(await gw.getGrid(home.gridID), rowKind, cx, cy)!;
    expect(stable(after), `the reload changed the stored ${kind} row`).toBe(stable(before));

    // And the meter is live: the first gesture after the restore does write.
    await gw.ascendViaCrumb();
    await gw.waitIdle();
    await gw.wheelAtFocusedCenter(-300);
    await settle(window, settleMs);
    await expect.poll(() => framingWrites(window), { timeout: 10_000 }).toBeGreaterThan(0);
  });
}
