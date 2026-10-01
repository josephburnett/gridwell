import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { tileAt, GridSnapshot } from './oracle';
import { settle } from './cadence';
import type { GridwellDriver } from './driver';
import { EV } from '../src/main/ipc';

// Esc lets go of any drag in flight with nothing changed: everything returns
// to where the press found it, and the release that follows is neither a drop
// nor a click (gesture.Escape). Two oracles per gesture, because either alone
// can pass while the other fails: the server's rows, and every mutating call
// the client sent, counted at the wire.

const WRITES = ['PlaceTile', 'CloneTile', 'CreateTile', 'SetTile', 'DeleteTile', 'SetFraming', 'WriteContent'];

async function watchWrites(win: Page): Promise<() => string[]> {
  const seen: string[] = [];
  await win.route('**/gridwell.v1.Gridwell/*', async (r) => {
    const m = r.request().url().split('/').pop() ?? '';
    if (WRITES.includes(m)) seen.push(m);
    await r.continue();
  });
  return () => seen.slice();
}

function armed(win: Page): Promise<{ dragging: boolean; leftResize: boolean; rightDrag: boolean }> {
  return win.evaluate(() => (window as any).__gridwellTest.idleDetail());
}

async function expectLetGo(win: Page) {
  expect(await armed(win), 'Esc dropped every arm').toEqual(
    expect.objectContaining({ dragging: false, leftResize: false, rightDrag: false }),
  );
}

function count(snap: GridSnapshot, kind: string): number {
  return (snap.tiles ?? []).filter((t) => t.kind === kind).length;
}

// A text tile the client holds, its landing over, so the press that follows
// grabs it and the drag's ghost is its own.
function ghostActive(win: Page): Promise<boolean> {
  return win.evaluate(() => (window as any).__gridwellTest.ghost().active);
}

async function textTile(gw: GridwellDriver, win: Page, cx: number, cy: number) {
  const f = await gw.focused();
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(t, 'created text tile').toBeTruthy();
  await expect.poll(async () => (await gw.focused()).tileIds.includes(t.id)).toBe(true);
  await expect.poll(() => ghostActive(win)).toBe(false);
  return t;
}

// The rest of the gesture after Esc: the button still held, a move, then the
// release, over a point the drop would have acted on.
async function finishHeld(win: Page, x: number, y: number, button: 'left' | 'right') {
  await win.mouse.move(x + 3, y + 3);
  await win.mouse.move(x, y);
  await win.mouse.up({ button });
}

test('Esc on a tile move leaves the tile in its cell and writes nothing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const before = await textTile(gw, window, cx, cy);
  const writes = await watchWrites(window);

  const from = await gw.cellCenter(f.id, cx, cy);
  const to = await gw.cellCenter(f.id, cx - 2, cy);
  await window.mouse.move(from.x, from.y);
  await window.mouse.down();
  await window.mouse.move(from.x + 8, from.y + 8);
  await window.mouse.move(to.x, to.y, { steps: 8 });
  expect(await ghostActive(window), 'a ghost in flight').toBe(true);
  await window.keyboard.press('Escape');
  await expectLetGo(window);
  await finishHeld(window, to.x, to.y, 'left');
  await gw.waitIdle();

  const after = (await gw.focused());
  expect(after.placeDepth, 'the release was not a click: no descent').toBe(0);
  expect(after.textFocus, 'the release was not a click: no descent').toBe('');
  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx, cy)?.id, 'the tile is still in its cell').toBe(before.id);
  expect(tileAt(snap, 'text', cx - 2, cy), 'nothing landed at the drop cell').toBeFalsy();
  await expect.poll(() => ghostActive(window), 'the ghost snapped back and went').toBe(false);
  expect(writes(), 'no write left the client').toEqual([]);
});

for (const link of [false, true]) {
  const what = link ? 'ctrl + right-drag link' : 'right-drag clone';
  test(`Esc on a ${what} makes no tile`, async ({ gw, window }) => {
    await gw.enterPlugin('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy);
    await textTile(gw, window, cx, cy);
    const n = count(await gw.getGrid(f.gridID), 'text');
    const writes = await watchWrites(window);

    const from = await gw.cellCenter(f.id, cx, cy);
    const to = await gw.cellCenter(f.id, cx, cy - 2);
    await window.mouse.move(from.x, from.y);
    if (link) await window.keyboard.down('Control');
    await window.mouse.down({ button: 'right' });
    if (link) await window.keyboard.up('Control');
    await window.mouse.move(from.x + 8, from.y + 8);
    await window.mouse.move(to.x, to.y, { steps: 8 });
    await window.keyboard.press('Escape');
    await expectLetGo(window);
    await finishHeld(window, to.x, to.y, 'right');
    await gw.waitIdle();

    const snap = await gw.getGrid(f.gridID);
    expect(count(snap, 'text'), 'no tile was made').toBe(n);
    expect(tileAt(snap, 'text', cx, cy - 2), 'nothing at the drop cell').toBeFalsy();
    expect(writes(), 'no write left the client').toEqual([]);
  });
}

test('Esc on a pan returns the viewport and stamps no framing', async ({ gw, window }) => {
  const settleMs = (await gw.cadences()).framingSaveMs;
  await gw.enterPlugin('home');
  await settle(window, settleMs);
  const f = await gw.focused();
  const posts = () =>
    window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetFraming ?? 0));
  const framing = await posts();
  const writes = await watchWrites(window);

  const from = await gw.cellCenter(f.id, Math.round(f.cx), Math.round(f.cy));
  await window.mouse.move(from.x, from.y);
  await window.mouse.down();
  await window.mouse.move(from.x + 8, from.y + 8);
  await window.mouse.move(from.x + 160, from.y + 90, { steps: 8 });
  const moved = await gw.focused();
  expect(Math.abs(moved.cx - f.cx), 'the pan moved the view').toBeGreaterThan(0.5);
  await window.keyboard.press('Escape');
  await expectLetGo(window);
  const back = await gw.focused();
  expect([back.cx, back.cy, back.zoom], 'Esc put the view back').toEqual([f.cx, f.cy, f.zoom]);
  await finishHeld(window, from.x + 160, from.y + 90, 'left');
  await gw.waitIdle();
  await settle(window, settleMs);

  const after = await gw.focused();
  expect([after.cx, after.cy, after.zoom], 'the view is where the press found it').toEqual([f.cx, f.cy, f.zoom]);
  expect(await posts(), 'the settle persister wrote no framing').toBe(framing);
  expect(writes(), 'no write left the client').toEqual([]);
});

test('Esc on a divider resize puts the panes back', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const writes = await watchWrites(window);
  const x = before[0].x + before[0].w - 2;
  const y = before[0].y + before[0].h / 2;

  await window.mouse.move(x, y);
  await window.mouse.down();
  await window.mouse.move(x - 8, y);
  await window.mouse.move(x + 150, y, { steps: 10 });
  const during = (await gw.panes()).find((p) => p.id === before[0].id)!;
  expect(during.w - before[0].w, 'the drag resized live').toBeGreaterThan(100);
  await window.keyboard.press('Escape');
  await expectLetGo(window);
  await finishHeld(window, x + 150, y, 'left');
  await gw.waitIdle();

  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after.map((p) => [p.id, p.x, p.w]), 'the panes are where the press found them').toEqual(
    before.map((p) => [p.id, p.x, p.w]),
  );
  expect(writes(), 'no write left the client').toEqual([]);
});

test('Esc on a crush keeps the pane', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before.length).toBe(2);
  const left = before[0];
  const y = left.y + left.h / 2;
  const x = left.x + left.w - 2;

  // Past the wall: the left pane is red and the release would close it.
  await window.mouse.move(x, y);
  await window.mouse.down();
  await window.mouse.move(x - 8, y);
  await window.mouse.move(left.x + 6, y, { steps: 10 });
  await window.keyboard.press('Escape');
  await expectLetGo(window);
  await finishHeld(window, left.x + 6, y, 'left');
  await gw.waitIdle();

  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after.map((p) => [p.id, p.x, p.w]), 'both panes survive at their press bounds').toEqual(
    before.map((p) => [p.id, p.x, p.w]),
  );
});

test('Esc on a swatch drag-create makes nothing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const n = (await gw.getGrid(f.gridID)).tiles?.length ?? 0;
  await gw.openPalette();
  const pal = await gw.palette();
  const item = pal.items.find((i) => !i.isPlugin && i.kind === 'markdown')!;
  const to = await gw.cellCenter(f.id, cx, cy);
  const writes = await watchWrites(window);

  const sx = item.x + item.w / 2;
  const sy = item.y + item.h / 2;
  await window.mouse.move(sx, sy);
  await window.mouse.down();
  await window.mouse.move(sx + 10, sy + 10);
  await window.mouse.move(to.x, to.y, { steps: 8 });
  await window.keyboard.press('Escape');
  await expectLetGo(window);
  await finishHeld(window, to.x, to.y, 'left');
  await gw.waitIdle();

  expect((await gw.getGrid(f.gridID)).tiles?.length ?? 0, 'no tile was made').toBe(n);
  expect(writes(), 'no write left the client').toEqual([]);
});

// The press, the move, Esc and the release in one synchronous turn, as in
// ephemeral-promote.spec.ts: arming the drag parks the live view, and the layer
// change under the real cursor would otherwise read as a lost release.
test('Esc on the ephemeral crumb drag promotes nothing', async ({ electronApp, window, gw }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  await gw.splitFocusedPaneVertical();
  const visitor = await gw.focused();
  const dest = (await gw.panes()).find((p) => p.id !== visitor.id)!;
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?keep=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), { timeout: 15_000 })
    .toBeGreaterThan(wcBefore);
  const visiting = await gw.focused();
  expect(visiting.textFocus, 'the visit is a descent').not.toBe('');
  const writes = await watchWrites(window);

  const bar = await gw.bar();
  const crumb = bar.segments[bar.segments.length - 1];
  const from = { x: crumb.x + crumb.w / 2, y: bar.top + bar.height / 2 };
  const to = await gw.cellCenter(dest.id, Math.round(dest.cx), Math.round(dest.cy));
  const held = await window.evaluate(
    ([fx, fy, tx, ty]: number[]) => {
      const canvas = document.querySelector('canvas')!;
      const fire = (type: string, x: number, y: number, buttons: number) =>
        canvas.dispatchEvent(
          new MouseEvent(type, { clientX: x, clientY: y, buttons, button: 0, bubbles: true }),
        );
      fire('mousedown', fx, fy, 1);
      fire('mousemove', tx, ty, 1);
      const d = (window as any).__gridwellTest.idleDetail().dragging;
      (globalThis as any).dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      fire('mousemove', tx + 3, ty + 3, 1);
      fire('mouseup', tx, ty, 0);
      return d;
    },
    [from.x, from.y, to.x, to.y],
  );
  expect(held, 'the crumb drag was armed').toBe(true);
  await expectLetGo(window);
  await gw.waitIdle();

  const urls = ((await gw.getGrid(home.gridID)).tiles ?? []).filter((t) => t.kind === 'url');
  expect(urls, 'no url tile was promoted onto the grid').toEqual([]);
  expect((await gw.focused()).textFocus, 'the pane is still on its visit').toBe(visiting.textFocus);
  expect(writes(), 'no write left the client').toEqual([]);
});

// With nothing in flight Esc is not a gesture's, and a drag after a cancelled
// one is a fresh gesture that commits as usual.
test('a drag after a cancelled one moves the tile', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await textTile(gw, window, cx, cy);
  await window.keyboard.press('Escape');
  const from = await gw.cellCenter(f.id, cx, cy);
  await window.mouse.move(from.x, from.y);
  await window.mouse.down();
  await window.mouse.move(from.x + 8, from.y + 8);
  await window.keyboard.press('Escape');
  await window.mouse.up();
  await gw.waitIdle();
  await gw.dragTileCell(cx, cy, cx - 2, cy);
  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx - 2, cy), 'the fresh drag moved the tile').toBeTruthy();
});

// A press over a live url view goes to the native view, which keeps OS
// keyboard focus through the drag it forwards, so the window's keydown never
// hears Esc. The drag parks the view, and a parked view hands Esc to the
// renderer. The forwarded press is fired from main as in
// live-border-drag.spec.ts, and the key goes to the view's own webContents,
// which is where the OS delivers it.
test('Esc reaches a drag forwarded from a live view that holds keyboard focus', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/?esc=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  const urlPaneId = (await gw.focused()).id;
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before[0].id, 'live url pane is the left pane').toBe(urlPaneId);
  const gx = before[0].x + before[0].w;
  const gy = before[0].y + before[0].h / 2;

  await electronApp.evaluate(
    ({ BrowserWindow, webContents }, { ch, pt }) => {
      const view = webContents.getAllWebContents().find((w) => w.getURL().includes('esc=1'));
      if (!view) throw new Error('live view not found');
      view.focus();
      BrowserWindow.getAllWindows()[0].webContents.send(ch, pt);
    },
    { ch: EV.leftForward, pt: { x: gx - 8, y: gy } },
  );
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.leftResizeArmed()), { timeout: 5_000 })
    .toBe(true);
  await window.evaluate(
    ([tx, ty]: number[]) => {
      document.querySelector('canvas')!.dispatchEvent(
        new MouseEvent('mousemove', { clientX: tx, clientY: ty, buttons: 1, bubbles: true }),
      );
    },
    [gx - 200, gy],
  );
  const during = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  expect(before[0].w - during.w, 'the drag resized live').toBeGreaterThan(100);

  const relays = () =>
    electronApp.evaluate(() => ((globalThis as any).__gwRegistry?.escapeRelays as number) ?? 0);
  // sendInputEvent is fire-and-forget and xvfb can drop it before the input
  // pipeline, so resend until main acks; see content-zoom.spec.ts.
  let acked = false;
  for (let attempt = 0; attempt < 5 && !acked; attempt++) {
    const n = await relays();
    await electronApp.evaluate(({ webContents }) => {
      const view = webContents.getAllWebContents().find((w) => w.getURL().includes('esc=1'))!;
      view.sendInputEvent({ type: 'keyDown', keyCode: 'Escape' });
      view.sendInputEvent({ type: 'keyUp', keyCode: 'Escape' });
    });
    acked = await expect
      .poll(relays, { timeout: 2_000 })
      .toBeGreaterThan(n)
      .then(
        () => true,
        () => false,
      );
  }
  expect(acked, 'Esc in the parked view reached main').toBe(true);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.leftResizeArmed()), { timeout: 5_000 })
    .toBe(false);
  await window.evaluate(
    ([tx, ty]: number[]) => {
      document.querySelector('canvas')!.dispatchEvent(
        new MouseEvent('mouseup', { clientX: tx, clientY: ty, button: 0, bubbles: true }),
      );
    },
    [gx - 200, gy],
  );
  await gw.waitIdle();
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after.map((p) => [p.id, p.x, p.w]), 'the panes are where the press found them').toEqual(
    before.map((p) => [p.id, p.x, p.w]),
  );
});
