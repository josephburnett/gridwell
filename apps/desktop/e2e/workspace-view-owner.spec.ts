import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt, updateText, writeContent } from './oracle';
import { settle } from './cadence';
import { dumpViaAPI, TraceLine } from './trace';

// Inside a pane tile a grid's view is the layout blob's (2026-10-14), and so
// is a text leaf's live scroll, while leaving it writes its row (2026-10-16). These cross the seam from the gesture
// to the node's own record of what it wrote, and from stored bytes to what a
// restored leaf shows.

async function workspaceDepth(window: any): Promise<number> {
  return (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
}

function storeWrites(lines: TraceLine[], verb: (msg: string) => boolean): number {
  return lines.filter((l) => l.src === 'store' && l.kind === 'write' && verb(l.msg)).length;
}
const isLayout = (m: string) => m === 'SetPaneLayout';
const isFraming = (m: string) => m.startsWith('SetFraming');

// The scroll of whichever text overlay is showing, -1 when neither is.
async function shownScroll(window: any): Promise<number> {
  return window.evaluate(() => {
    for (const id of ['gw-text-editor', 'gw-rendered-view']) {
      const el = document.getElementById(id);
      if (el && el.style.display !== 'none') return el.scrollTop;
    }
    return -1;
  });
}

function rpcStarts(lines: TraceLine[], verb: string): number {
  return lines.filter((l) => l.src === 'router' && l.kind === 'rpc' && l.msg === `${verb} start`).length;
}

test('a pan inside a pane tile writes the layout blob once and no framing row, and the round trip returns to it', async ({
  gw,
  home,
  window,
}) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  await gw.openMenu();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  const well = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy);
  expect(pt && well, 'pane tile and well persisted').toBeTruthy();

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.descendCell(wx + 1, wy);
  const childGrid = (await gw.focused()).gridID;
  expect(childGrid, 'descended into the well inside the pane tile').not.toBe(rootGrid);
  await expect
    .poll(async () => (await gw.getTileContent(pt!.id)).includes(well!.id), {
      message: 'the layout records the descent, which is arrangement',
      timeout: 10_000,
    })
    .toBe(true);
  // The zoom settles on its own, so what follows counts the pan alone. The
  // child is empty, so the pan press lands on no tile.
  await gw.wheelAtFocusedCenter(-300);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs) * 2);

  const before = await dumpViaAPI(gw.origin, token);
  const layoutBefore = storeWrites(before, isLayout);
  const writeContentBefore = rpcStarts(before, 'WriteContent');
  const framingBefore = storeWrites(before, isFraming);

  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();

  await expect
    .poll(async () => storeWrites(await dumpViaAPI(gw.origin, token), isLayout), {
      message: 'the pan settles into one layout write',
      timeout: 10_000,
    })
    .toBeGreaterThan(layoutBefore);
  // Two more windows of each, so any write armed by the pan has landed.
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs) * 2);
  const after = await dumpViaAPI(gw.origin, token);
  expect(storeWrites(after, isLayout), 'one pan, one layout write').toBe(layoutBefore + 1);
  expect(rpcStarts(after, 'WriteContent'), 'one pan, one WriteContent').toBe(writeContentBefore + 1);
  expect(storeWrites(after, isFraming), 'a pan inside a pane tile wrote a framing row').toBe(framingBefore);
  const layoutAfterPan = storeWrites(after, isLayout);

  // Out of the pane tile and back in: the blob is read again, and the leaf
  // shows the grid where the blob says it was left.
  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(childGrid);
  await expect.poll(async () => (await gw.focused()).zoom, { message: 'zoom round-tripped' })
    .toBeCloseTo(left.zoom, 1);
  const back = await gw.focused();
  expect(back.cx, 'center x round-tripped').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y round-tripped').toBeCloseTo(left.cy, 1);

  const end = await dumpViaAPI(gw.origin, token);
  expect(storeWrites(end, isLayout), 'the round trip wrote the layout blob').toBe(layoutAfterPan);
  await gw.leaveWorkspace();
});

test("an old blob's retired viewport is not read: a leaf restores to its grid's framing row", async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  await gw.openMenu();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  const well = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy);
  expect(pt && well, 'pane tile and well persisted').toBeTruthy();

  // Frame the well's grid outside any pane tile, so its row owns a view.
  await gw.descendCell(wx + 1, wy);
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  await gw.middleClickCell(Math.round(left.cx), Math.round(left.cy));
  expect((await gw.focused()).gridID, 'ascended out of the child').toBe(rootGrid);
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy)?.viewZoom ?? 0), {
      message: 'the ascent wrote the well row',
    })
    .toBeGreaterThan(0);

  // A blob as every Gridwell before this one wrote it: the leaf on the well's
  // grid, with a viewport of its own that disagrees with the row.
  const blobCx = left.cx + 37;
  const blobCy = left.cy - 23;
  const old = JSON.stringify({
    v: 1,
    root: { pane: { id: 'p1', anchor: left.anchor, path: left.path, cx: blobCx, cy: blobCy, zoom: 2.5 } },
    focus: 'p1',
  });
  await writeContent(gw.origin, pt!.id, 0, Buffer.from(old));

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(left.gridID);
  await expect
    .poll(async () => (await gw.focused()).cx, { message: "the leaf shows the row's center, not the blob's" })
    .toBeCloseTo(left.cx, 1);
  const shown = await gw.focused();
  expect(shown.cy).toBeCloseTo(left.cy, 1);
  expect(shown.zoom).toBeCloseTo(left.zoom, 1);
  expect(shown.cx).not.toBeCloseTo(blobCx, 0);
  await gw.leaveWorkspace();
});

test("a text leaf's scroll is the layout's: a scroll writes the blob and no SetTextScroll, the mode reaches the row, and re-entry restores both", async ({
  gw,
  home,
  window,
}) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const c = await gw.cadences();
  const settleMs = Math.max(c.workspaceSaveMs, c.framingSaveMs);
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  await gw.openMenu();
  await gw.dragCreate('markdown', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;
  const doc = tileAt(await gw.getGrid(rootGrid), 'text', wx + 1, wy)!;
  await updateText(gw.origin, doc.id, Number(doc.version ?? 0), '# long\n\n' + 'line\n\n'.repeat(200));

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.descendCell(wx + 1, wy);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(doc.id);
  await expect
    .poll(async () => (await gw.getTileContent(pt.id)).includes(doc.id), { timeout: 10_000 })
    .toBe(true);
  await settle(window, settleMs);

  // The mode is the row's, so the toggle reaches it: that is how the tile
  // presents from outside.
  const row = async () => tileAt(await gw.getGrid(rootGrid), 'text', wx + 1, wy) as Record<string, unknown>;
  const opened = (await gw.focused()).textMode;
  await gw.toggleTextMode();
  await expect.poll(async () => (await gw.focused()).textMode).not.toBe(opened);
  const mode = (await gw.focused()).textMode;
  await expect.poll(async () => String((await row()).textMode ?? ''), { message: 'the toggle reached the row' })
    .toBe(mode);
  await settle(window, settleMs);

  const textScrolls = () =>
    window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetTextScroll ?? 0));
  const textScrollsBefore = await textScrolls();
  const layoutBefore = storeWrites(await dumpViaAPI(gw.origin, token), isLayout);
  const rowBefore = Number((await row()).textY ?? 0);

  const p = await gw.focused();
  await window.mouse.move(p.x + p.w / 2, p.y + p.h / 2);
  for (let i = 0; i < 8; i++) await window.mouse.wheel(0, 120);
  await expect
    .poll(async () => storeWrites(await dumpViaAPI(gw.origin, token), isLayout), {
      message: 'the scroll settles into a layout write',
      timeout: 10_000,
    })
    .toBeGreaterThan(layoutBefore);
  await settle(window, settleMs * 2);
  expect(await textScrolls(), 'a scroll inside a pane tile posted SetTextScroll').toBe(textScrollsBefore);
  expect(Number((await row()).textY ?? 0), "a scroll inside a pane tile moved the row's scroll").toBe(rowBefore);
  const scrolled = await shownScroll(window);
  expect(scrolled, 'the wheel scrolled the document').toBeGreaterThan(0);

  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(doc.id);
  await expect.poll(async () => (await gw.focused()).textMode, { message: "re-entry shows the row's mode" })
    .toBe(mode);
  await expect
    .poll(() => shownScroll(window), { message: "re-entry shows the layout's scroll" })
    .toBeCloseTo(scrolled, -1);
  await gw.leaveWorkspace();
});

test('inside a pane tile a document scrolled and left reopens where it was left, and only the leave wrote its row', async ({
  gw,
  window,
}) => {
  const c = await gw.cadences();
  const settleMs = Math.max(c.workspaceSaveMs, c.framingSaveMs);
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('pane', wx, wy);
  await gw.openMenu();
  await gw.dragCreate('markdown', wx + 1, wy);
  const doc = tileAt(await gw.getGrid(rootGrid), 'text', wx + 1, wy)!;
  await updateText(gw.origin, doc.id, Number(doc.version ?? 0), '# long\n\n' + 'line\n\n'.repeat(200));
  const row = async () => tileAt(await gw.getGrid(rootGrid), 'text', wx + 1, wy) as Record<string, unknown>;

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.descendCell(wx + 1, wy);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(doc.id);
  await gw.toggleTextMode();
  await expect.poll(async () => (await gw.focused()).textMode).toBe('rendered');
  await settle(window, settleMs);

  const textScrolls = () =>
    window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetTextScroll ?? 0));
  const before = await textScrolls();
  const p = await gw.focused();
  await window.mouse.move(p.x + p.w / 2, p.y + p.h / 2);
  for (let i = 0; i < 8; i++) await window.mouse.wheel(0, 120);
  await expect.poll(() => shownScroll(window)).toBeGreaterThan(0);
  await settle(window, settleMs * 2);
  const scrolled = await shownScroll(window);
  expect(await textScrolls(), 'the scroll settle posted SetTextScroll before any leave').toBe(before);
  expect(Number((await row()).textY ?? 0), 'the settle moved the row').toBe(0);

  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => Number((await row()).textY ?? 0), { message: 'the leave wrote where the document was left' })
    .toBeCloseTo(scrolled, -1);

  await gw.descendCell(wx + 1, wy);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(doc.id);
  await expect
    .poll(() => shownScroll(window), { message: 'the document reopened where it was left' })
    .toBeCloseTo(scrolled, -1);
  await gw.leaveWorkspace();
});
