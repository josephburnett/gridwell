import type { ElectronApplication, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver, PaneInfo } from './driver';
import { createText, getTile, handshake } from './oracle';

// An ephemeral visit's crumb drags as the tile it is (Joe, 2026-11-10: "That
// behavior should be consistent"): a left-drag moves the row onto the grid it
// lands on and the visiting pane follows it, a right-drag copies it, a link
// to it is refused, and the trashcan ends the visit as the ascent does.

type Button = 'left' | 'right';

// The press on the visit's crumb, one move and the release, in one
// synchronous turn. Arming the drag parks every live view, which changes what
// sits under the REAL cursor, and Playwright drives a virtual mouse, so the
// real cursor is still at the screen center. Chromium reports that layer
// change as a mousemove with no button held, which is a lost release
// (recoverLostRelease) and would end the drag wherever the real cursor sits.
// Press, move and release together leave no window for it.
async function dragCrumb(
  window: Page,
  gw: GridwellDriver,
  tileID: string,
  to: { x: number; y: number },
  button: Button = 'left',
  ctrl = false,
  anchor = '',
): Promise<void> {
  const bar = await gw.bar();
  // A door visit's frame carries its grid, so its crumb is the grid's.
  const crumb = bar.segments.find((s) => s.tileID === tileID || (s.anchor !== '' && s.anchor === anchor));
  expect(crumb, `the bar has the visit's crumb ${tileID}`).toBeTruthy();
  const from = { x: crumb!.x + crumb!.w / 2, y: bar.top + bar.height / 2 }; // segment x is absolute
  await window.evaluate(
    ([fx, fy, tx, ty, b, c]: number[]) => {
      const canvas = document.querySelector('canvas')!;
      const fire = (type: string, x: number, y: number, buttons: number) =>
        canvas.dispatchEvent(
          new MouseEvent(type, { clientX: x, clientY: y, buttons, button: b, ctrlKey: c === 1, bubbles: true }),
        );
      const held = b === 2 ? 2 : 1;
      fire('mousedown', fx, fy, held);
      fire('mousemove', (fx + tx) / 2, (fy + ty) / 2, held);
      fire('mousemove', tx, ty, held);
      fire('mouseup', tx, ty, 0);
    },
    [from.x, from.y, to.x, to.y, button === 'right' ? 2 : 0, ctrl ? 1 : 0],
  );
  await gw.waitIdle();
}

// Two panes on the home grid; the visit happens in the focused one.
async function twoPanes(gw: GridwellDriver): Promise<{ home: PaneInfo; dest: PaneInfo }> {
  await gw.enterSource('home');
  const home = await gw.focused();
  await gw.splitFocusedPaneVertical();
  const visitor = await gw.focused();
  const dest = (await gw.panes()).find((p) => p.id !== visitor.id)!;
  return { home, dest };
}

async function visitURL(electronApp: ElectronApplication, window: Page, gw: GridwellDriver, url: string): Promise<string> {
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickMenuSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', url);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), { timeout: 15_000 })
    .toBeGreaterThan(wcBefore);
  const id = (await gw.focused()).textFocus;
  expect(id, 'the visit is a descent').not.toBe('');
  return id;
}

// A click on a template in the strip visits an ephemeral copy of it.
async function visitTemplate(window: Page, gw: GridwellDriver, templateID: string): Promise<void> {
  await gw.openMenu();
  await gw.expandSources();
  let item: { x: number; y: number; w: number; h: number } | undefined;
  await expect
    .poll(async () => {
      item = (await gw.menu()).items.find((i) => i.template === templateID);
      return !!item;
    }, { message: 'the strip shows the template', timeout: 10_000 })
    .toBe(true);
  await window.mouse.click(item!.x + item!.w / 2, item!.y + item!.h / 2);
  await gw.waitIdle();
}

async function templatesGrid(window: Page): Promise<string> {
  const id = (await handshake(new URL(window.url()).origin)).templatesGridId ?? '';
  expect(id, 'the handshake names the templates grid').not.toBe('');
  return id;
}

const onGrid = async (gw: GridwellDriver, grid: string, id: string) =>
  ((await gw.getGrid(grid)).tiles ?? []).find((t) => t.id === id);

const notices = async (window: Page): Promise<string[]> =>
  ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).map((n: any) => String(n.message));

test('a url visit moved off its crumb keeps its id and its address, and the pane follows it', async ({ electronApp, window, gw }) => {
  const { home, dest } = await twoPanes(gw);
  const scratch = await gw.scratchGridID('home');
  const visit = await visitURL(electronApp, window, gw, `${gw.origin}/wasm_exec.js?keep=1`);
  const live = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);

  // Off the destination's center, so the cell pins which pane the drop
  // resolved in: the visiting pane frames the same grid at another zoom.
  const dropX = Math.round(dest.cx) + 2;
  const dropY = Math.round(dest.cy) + 1;
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, dropX, dropY));

  await expect.poll(async () => (await onGrid(gw, home.gridID, visit))?.id, { timeout: 15_000 }).toBe(visit);
  const row = (await onGrid(gw, home.gridID, visit))!;
  expect([Number(row.x ?? 0), Number(row.y ?? 0)], 'it landed on the cell the drop resolved').toEqual([dropX, dropY]);
  expect(String(row.urlString ?? ''), 'its address came with it').toContain('wasm_exec.js?keep=1');
  expect(await onGrid(gw, scratch, visit), 'it left the scratch grid').toBeUndefined();
  const f = await gw.focused();
  expect(f.textFocus, 'the pane is still on the row').toBe(visit);
  expect(f.gridID, 'the pane follows it to its grid').toBe(home.gridID);
  await expect.poll(async () => (await gw.barName()).label).not.toMatch(/ — ephemeral$/);
  expect(await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), 'the page stayed live').toBe(live);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test('a door visit moved onto a grid keeps its grid and what is on it, and the pane stays inside', async ({ window, gw }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const res = await fetch(`${origin}/gridwell.v1.Gridwell/CreateTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', Cookie: (await window.context().cookies()).map((c) => `${c.name}=${c.value}`).join('; ') },
    body: JSON.stringify({ gridId: tg, tile: { kind: 'door', x: 0, y: 0, w: 1, h: 1, altText: 'a door' } }),
  });
  expect(res.ok, 'the door template was made').toBe(true);
  const door = ((await res.json()) as { tile: { id: string } }).tile;
  const { home, dest } = await twoPanes(gw);
  const scratch = await gw.scratchGridID('home');
  await visitTemplate(window, gw, door.id);
  const copy = ((await gw.getGrid(scratch)).tiles ?? []).find((t) => t.kind === 'door')!;
  expect(copy, 'the visit is a copy on the scratch grid').toBeTruthy();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(copy.childGridId);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
  const inside = await createText(origin, copy.childGridId!, 1, 1, 'put inside the visit');
  const before = await gw.focused();
  await gw.waitClientTileAt(before.id, 1, 1, inside.id);

  const dropX = Math.round(dest.cx) - 1;
  const dropY = Math.round(dest.cy);
  await dragCrumb(window, gw, copy.id, await gw.cellCenter(dest.id, dropX, dropY), 'left', false, copy.childGridId);

  await expect.poll(async () => (await onGrid(gw, home.gridID, copy.id))?.childGridId, { timeout: 15_000 }).toBe(copy.childGridId);
  expect(await onGrid(gw, copy.childGridId!, inside.id), 'what was put inside it is still there').toBeTruthy();
  const f = await gw.focused();
  expect(f.id, 'the same pane').toBe(before.id);
  expect(f.gridID, 'the pane is still inside the door').toBe(copy.childGridId);
  expect([f.cx, f.cy, f.zoom], 'at the framing it had').toEqual([before.cx, before.cy, before.zoom]);
  await expect.poll(async () => (await gw.barName()).label).not.toMatch(/ — ephemeral$/);

  // It is a tile like any other now: leaving retires nothing.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(home.gridID);
  await gw.waitIdle();
  expect((await onGrid(gw, home.gridID, copy.id))?.id, 'the ascent kept it').toBe(copy.id);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test('a text visit from a template moves, a right-drag copies a visit, and ctrl + right-drag is refused', async ({ window, gw }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const tpl = await createText(origin, tg, 0, 0, 'a text to visit');
  const { home, dest } = await twoPanes(gw);
  const scratch = await gw.scratchGridID('home');
  const cx = Math.round(dest.cx);
  const cy = Math.round(dest.cy);

  await visitTemplate(window, gw, tpl.id);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const visit = (await gw.focused()).textFocus;
  expect(await onGrid(gw, scratch, visit), 'the visit is on the scratch grid').toBeTruthy();

  // A right-drag copies the row and leaves the visit as it was.
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx + 2, cy), 'right');
  await expect
    .poll(async () => ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => Number(t.x ?? 0) === cx + 2 && Number(t.y ?? 0) === cy)?.kind, { timeout: 15_000 })
    .toBe('text');
  const copied = ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => Number(t.x ?? 0) === cx + 2 && Number(t.y ?? 0) === cy)!;
  expect(copied.id, 'a copy, not the visit').not.toBe(visit);
  expect(await onGrid(gw, scratch, visit), 'the visit stayed').toBeTruthy();
  expect((await gw.focused()).textFocus, 'the pane is still on the visit').toBe(visit);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);

  // A link would die with the visit, so ctrl + right-drag is refused.
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx - 2, cy), 'right', true);
  await expect.poll(() => notices(window)).toContain('a link to an ephemeral visit would die with it');
  expect(((await gw.getGrid(home.gridID)).tiles ?? []).length, 'no link was made').toBe(n);
  expect(await onGrid(gw, scratch, visit), 'the visit stayed').toBeTruthy();

  // A left-drag moves it, and it is the same row.
  const before = await getTile(origin, visit);
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx, cy + 2));
  await expect.poll(async () => (await onGrid(gw, home.gridID, visit))?.id, { timeout: 15_000 }).toBe(visit);
  const moved = await getTile(origin, visit);
  expect(moved.version, 'a move changes no content').toBe(before.version);
  const f = await gw.focused();
  expect([f.textFocus, f.gridID], 'the pane follows it').toEqual([visit, home.gridID]);
  await expect.poll(async () => (await gw.barName()).label).not.toMatch(/ — ephemeral$/);
});

test('a visit dropped on the trashcan ends as the ascent ends it', async ({ window, gw }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const tpl = await createText(origin, tg, 0, 0, 'a text to throw away');
  await gw.enterSource('home');
  const home = await gw.focused();
  const scratch = await gw.scratchGridID('home');
  await visitTemplate(window, gw, tpl.id);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const visit = (await gw.focused()).textFocus;

  await dragCrumb(window, gw, visit, await gw.circleCenter());
  await expect.poll(async () => (await gw.focused()).gridID).toBe(home.gridID);
  expect((await gw.focused()).textFocus, 'the pane ascended out of it').toBe('');
  await expect.poll(async () => await onGrid(gw, scratch, visit), { timeout: 10_000 }).toBeUndefined();
  expect(await onGrid(gw, tg, tpl.id), 'the template stayed').toBeTruthy();
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test.describe('across a connection', () => {
  test.use({ extraNodes: ['second'] });

  // There is no move across a connection, and the link a left-drag makes
  // there would die with the visit, so the move is refused and says so
  // (Joe, 2026-11-11), as information.
  test('a left-drag of a visit onto a far grid is refused as the move it is', async ({ window, gw }) => {
    const origin = new URL(window.url()).origin;
    const tg = await templatesGrid(window);
    const tpl = await createText(origin, tg, 0, 0, 'a text that stays home');
    const { home, dest } = await twoPanes(gw);
    const visitor = await gw.focused();
    await gw.clickScreen(dest.x + 20, dest.y + 20);
    await gw.enterSource('second');
    const far = (await gw.panes()).find((p) => p.id === dest.id)!;
    expect(far.gridID, 'the other pane is on the far home').not.toBe(home.gridID);
    await gw.clickScreen(visitor.x + 20, visitor.y + 20);
    const scratch = await gw.scratchGridID('home');

    await visitTemplate(window, gw, tpl.id);
    await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
    const visit = (await gw.focused()).textFocus;
    const n = ((await gw.getGrid(far.gridID)).tiles ?? []).length;

    await dragCrumb(window, gw, visit, await gw.cellCenter(far.id, Math.round(far.cx), Math.round(far.cy)));
    const told = async () =>
      ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).find(
        (x: any) => x.message === 'an ephemeral visit does not move across a connection',
      )?.severity ?? null;
    await expect.poll(told).toBe('info');
    expect(((await gw.getGrid(far.gridID)).tiles ?? []).length, 'nothing landed on the far grid').toBe(n);
    expect(await onGrid(gw, scratch, visit), 'the visit stayed').toBeTruthy();
    expect((await gw.focused()).textFocus, 'the pane is still on the visit').toBe(visit);
  });
});
