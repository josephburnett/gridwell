import type { ElectronApplication, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver, PaneInfo } from './driver';
import { createText, getTile, handshake } from './oracle';

// An ephemeral visit's crumb is its tile as every crumb is (Joe, 2026-11-19:
// "Right button only, all crumbs"): a right-drag copies it onto a grid and
// the visit stays as it was, a link to it is refused, and a left-drag or the
// trashcan moves and deletes nothing, the trashcan saying why.

type Button = 'left' | 'right';

// The press on the visit's crumb, one move and the release, in one
// synchronous turn. Arming the drag parks every live view, which changes what
// sits under the REAL cursor, and Playwright drives a virtual mouse, so the
// real cursor is still at the screen center. Chromium reports that layer
// change as a mousemove with no button held, which is a lost release
// (gesture.RecoverRelease) and would end the drag wherever the real cursor sits.
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

// The visit as it stands: on the scratch grid, the focused pane on it, grey.
async function stillAVisit(gw: GridwellDriver, scratch: string, visit: string, field: 'textFocus' | 'gridID' = 'textFocus', want = visit) {
  expect(await onGrid(gw, scratch, visit), 'the visit is still on the scratch grid').toBeTruthy();
  expect((await gw.focused())[field], 'the pane is still on the visit').toBe(want);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
}

test('a url visit right-dragged off its crumb is copied, and a left-drag moves nothing', async ({ electronApp, window, gw }) => {
  const { home, dest } = await twoPanes(gw);
  const scratch = await gw.scratchGridID('home');
  const visit = await visitURL(electronApp, window, gw, `${gw.origin}/wasm_exec.js?keep=1`);
  const live = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;

  // Off the destination's center, so the cell pins which pane the drop
  // resolved in: the visiting pane frames the same grid at another zoom.
  const dropX = Math.round(dest.cx) + 2;
  const dropY = Math.round(dest.cy) + 1;
  const at = await gw.cellCenter(dest.id, dropX, dropY);

  // A left-drag is the current crumb's click, which does nothing.
  await dragCrumb(window, gw, visit, at);
  expect(((await gw.getGrid(home.gridID)).tiles ?? []).length, 'nothing landed').toBe(n);
  await stillAVisit(gw, scratch, visit);

  await dragCrumb(window, gw, visit, at, 'right');
  const landed = async () => ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => Number(t.x ?? 0) === dropX && Number(t.y ?? 0) === dropY);
  await expect.poll(async () => (await landed())?.kind, { timeout: 15_000 }).toBe('url');
  const copy = (await landed())!;
  expect(copy.id, 'a copy, not the visit').not.toBe(visit);
  expect(String(copy.urlString ?? ''), 'its address came with it').toContain('wasm_exec.js?keep=1');
  await stillAVisit(gw, scratch, visit);
  expect(await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), 'the page stayed live').toBe(live);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test('a door visit right-dragged onto a grid is copied with what is on it, and the pane stays inside the visit', async ({ window, gw }) => {
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
  const visit = ((await gw.getGrid(scratch)).tiles ?? []).find((t) => t.kind === 'door')!;
  expect(visit, 'the visit is a copy on the scratch grid').toBeTruthy();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(visit.childGridId);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
  const inside = await createText(origin, visit.childGridId!, 1, 1, 'put inside the visit');
  const before = await gw.focused();
  await gw.waitClientTileAt(before.id, 1, 1, inside.id);

  const dropX = Math.round(dest.cx) - 1;
  const dropY = Math.round(dest.cy);
  const at = await gw.cellCenter(dest.id, dropX, dropY);
  await dragCrumb(window, gw, visit.id, at, 'left', false, visit.childGridId);
  expect(await onGrid(gw, home.gridID, visit.id), 'a left-drag moved nothing').toBeUndefined();

  await dragCrumb(window, gw, visit.id, at, 'right', false, visit.childGridId);
  const landed = async () => ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => Number(t.x ?? 0) === dropX && Number(t.y ?? 0) === dropY);
  await expect.poll(async () => (await landed())?.kind, { timeout: 15_000 }).toBe('door');
  const copy = (await landed())!;
  expect(copy.id, 'a copy, not the visit').not.toBe(visit.id);
  expect(copy.childGridId, 'a grid of its own').not.toBe(visit.childGridId);
  await expect
    .poll(async () => ((await gw.getGrid(copy.childGridId!)).tiles ?? []).some((t) => t.kind === 'text'), { timeout: 10_000 })
    .toBe(true);
  expect(await onGrid(gw, visit.childGridId!, inside.id), 'the visit still holds what was put inside it').toBeTruthy();
  const f = await gw.focused();
  expect([f.id, f.gridID], 'the same pane, still inside the visit').toEqual([before.id, visit.childGridId]);
  expect([f.cx, f.cy, f.zoom], 'at the framing it had').toEqual([before.cx, before.cy, before.zoom]);
  expect(await onGrid(gw, scratch, visit.id), 'the visit is still on the scratch grid').toBeTruthy();
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test('a text visit from a template is copied by a right-drag, and ctrl + right-drag and a left-drag are not', async ({ window, gw }) => {
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
  const before = await getTile(origin, visit);

  // A right-drag copies the row and leaves the visit as it was.
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx + 2, cy), 'right');
  await expect
    .poll(async () => ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => Number(t.x ?? 0) === cx + 2 && Number(t.y ?? 0) === cy)?.kind, { timeout: 15_000 })
    .toBe('text');
  const copied = ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => Number(t.x ?? 0) === cx + 2 && Number(t.y ?? 0) === cy)!;
  expect(copied.id, 'a copy, not the visit').not.toBe(visit);
  await stillAVisit(gw, scratch, visit);

  // A link would die with the visit, so ctrl + right-drag is refused.
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx - 2, cy), 'right', true);
  await expect.poll(() => notices(window)).toContain('a link to an ephemeral visit would die with it');
  expect(((await gw.getGrid(home.gridID)).tiles ?? []).length, 'no link was made').toBe(n);
  await stillAVisit(gw, scratch, visit);

  // A left-drag is the crumb's click: nothing moves.
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx, cy + 2));
  expect(((await gw.getGrid(home.gridID)).tiles ?? []).length, 'nothing landed').toBe(n);
  expect((await getTile(origin, visit)).version, 'the visit is as it was').toBe(before.version);
  await stillAVisit(gw, scratch, visit);
});

test('a visit dragged onto the trashcan stays, and says why', async ({ window, gw }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const tpl = await createText(origin, tg, 0, 0, 'a text to keep');
  await gw.enterSource('home');
  const scratch = await gw.scratchGridID('home');
  await visitTemplate(window, gw, tpl.id);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const visit = (await gw.focused()).textFocus;

  // A left-drag is the crumb's click, which does nothing and says nothing.
  await dragCrumb(window, gw, visit, await gw.circleCenter());
  await stillAVisit(gw, scratch, visit);
  expect(await notices(window), 'a click is no refusal').toEqual([]);
  await dragCrumb(window, gw, visit, await gw.circleCenter(), 'right');
  await stillAVisit(gw, scratch, visit);
  await expect.poll(() => notices(window)).toEqual(["a crumb's drag copies; it does not delete"]);
  expect(await onGrid(gw, tg, tpl.id), 'the template stayed').toBeTruthy();
});

// A workspace tile's visit drags from its boundary band as any crumb does:
// a right-drag onto a grid one of its panes shows copies it, layout and all,
// and the window stays inside the visit.
test('a workspace visit right-dragged off its boundary crumb is copied with its layout', async ({ window, gw }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const res = await fetch(`${origin}/gridwell.v1.Gridwell/CreateTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', Cookie: (await window.context().cookies()).map((c) => `${c.name}=${c.value}`).join('; ') },
    body: JSON.stringify({ gridId: tg, tile: { kind: 'workspace', x: 0, y: 0, w: 1, h: 1, altText: 'a workspace' } }),
  });
  expect(res.ok, 'the workspace template was made').toBe(true);
  await gw.enterSource('home');
  const home = await gw.focused();
  const scratch = await gw.scratchGridID('home');
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  const text = await createText(origin, home.gridID, cx - 2, cy, 'a text inside');

  await visitTemplate(window, gw, ((await gw.getGrid(tg)).tiles ?? [])[0].id);
  const depth = async () => (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
  await expect.poll(depth, { timeout: 15_000 }).toBe(1);
  const ws = ((await gw.getGrid(scratch)).tiles ?? []).find((t) => t.kind === 'workspace')!;
  expect(ws, 'the visit is on the scratch grid').toBeTruthy();

  // Two panes inside, one in the text, so the layout holds it.
  await gw.splitFocusedPaneVertical();
  await gw.descendCell(cx - 2, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).toBe(text.id);
  await expect.poll(async () => (await gw.getTileContent(ws.id)).includes(text.id), { timeout: 10_000 }).toBe(true);
  const inner = await gw.focused();
  const dest = (await gw.panes()).find((p) => p.id !== inner.id)!;
  expect(dest.gridID, 'the other pane shows home').toBe(home.gridID);
  const before = await getTile(origin, ws.id);

  const dropX = Math.round(dest.cx) + 1;
  const dropY = Math.round(dest.cy) + 1;
  const at = await gw.cellCenter(dest.id, dropX, dropY);
  await dragCrumb(window, gw, ws.id, at);
  expect(await onGrid(gw, home.gridID, ws.id), 'a left-drag moved nothing').toBeUndefined();
  expect(await depth(), 'and left nothing').toBe(1);

  await dragCrumb(window, gw, ws.id, at, 'right');
  const landed = async () => ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => Number(t.x ?? 0) === dropX && Number(t.y ?? 0) === dropY);
  await expect.poll(async () => (await landed())?.kind, { timeout: 15_000 }).toBe('workspace');
  const copy = (await landed())!;
  expect(copy.id, 'a copy, not the visit').not.toBe(ws.id);
  expect(await gw.getTileContent(copy.id), 'the copy holds the layout').toContain(text.id);
  expect(await onGrid(gw, scratch, ws.id), 'the visit is still on the scratch grid').toBeTruthy();
  expect((await getTile(origin, ws.id)).version, 'the visit is as it was').toBe(before.version);
  expect(await depth(), 'still inside').toBe(1);
  expect((await gw.focused()).textFocus, 'the panes are as they were').toBe(text.id);

  // Over the trashcan the band snaps back and says why.
  expect(await notices(window), 'nothing went wrong').toEqual([]);
  await dragCrumb(window, gw, ws.id, await gw.circleCenter(), 'right');
  await expect.poll(() => notices(window)).toEqual(["a crumb's drag copies; it does not delete"]);
  expect(await depth(), 'still inside').toBe(1);
  expect(await onGrid(gw, scratch, ws.id), 'the visit is still on the scratch grid').toBeTruthy();

  // Leaving retires the visit and keeps the copy.
  await gw.leaveWorkspace();
  await expect.poll(depth).toBe(0);
  await expect.poll(async () => await onGrid(gw, scratch, ws.id), { timeout: 10_000 }).toBeUndefined();
  expect((await onGrid(gw, home.gridID, copy.id))?.id, 'the copy stayed').toBe(copy.id);
});
