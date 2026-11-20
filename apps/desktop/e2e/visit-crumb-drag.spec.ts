import type { ElectronApplication, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver, PaneInfo } from './driver';
import { createLeafLink, createText, getTile, handshake, trashed } from './oracle';

// An ephemeral visit's crumb right-dragged onto a grid moves the visit's row
// there (Joe, 2026-11-19: "what was ephemeral is now persistent. And the pane
// stay as-is and I don't lose changes"): one row, the same id, the pane on it
// as it was and no longer grey. A ctrl + right-drag links to it as to any
// tile, a move across a connection is refused, and a left-drag or the
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
async function stillAVisit(gw: GridwellDriver, scratch: string, visit: string) {
  expect(await onGrid(gw, scratch, visit), 'the visit is still on the scratch grid').toBeTruthy();
  expect((await gw.focused()).textFocus, 'the pane is still on the visit').toBe(visit);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
}

// Typing goes to owner once the drop's landing has settled: until then the
// keyboard is the canvas's (gesture.Keyboard), for any drop.
async function typesInto(window: Page, owner: string) {
  await expect
    .poll(() => window.evaluate(() => {
      const k = (window as any).__gridwellTest.keyboard();
      return k.settled && k.holds ? k.owner : '';
    }))
    .toBe(owner);
}

// The visit kept: its one row on grid at (x, y), none left on the scratch
// grid, no second row made, and the bar no longer grey.
async function kept(gw: GridwellDriver, scratch: string, grid: string, id: string, x: number, y: number, n: number) {
  await expect.poll(async () => (await onGrid(gw, grid, id))?.id, { timeout: 15_000 }).toBe(id);
  const row = (await onGrid(gw, grid, id))!;
  expect([Number(row.x ?? 0), Number(row.y ?? 0)], 'it landed on the cell the drop resolved').toEqual([x, y]);
  expect(await onGrid(gw, scratch, id), 'it left the scratch grid').toBeUndefined();
  expect(((await gw.getGrid(grid)).tiles ?? []).length, 'one row, no copy').toBe(n + 1);
  await expect.poll(async () => (await gw.barName()).label).not.toMatch(/ — ephemeral$/);
  return row;
}

test('a url visit right-dragged off its crumb is kept: its id, its address, its live page', async ({ electronApp, window, gw }) => {
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
  const row = await kept(gw, scratch, home.gridID, visit, dropX, dropY, n);
  expect(String(row.urlString ?? ''), 'its address came with it').toContain('wasm_exec.js?keep=1');
  const f = await gw.focused();
  expect([f.textFocus, f.gridID], 'the pane is still on the row, on its grid').toEqual([visit, home.gridID]);
  expect(await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), 'the page stayed live').toBe(live);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test('a text visit right-dragged off its crumb is kept with what was typed before and after', async ({ window, gw }) => {
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
  if ((await gw.textareaInfo())?.tileID !== visit) await gw.toggleTextMode();
  await expect.poll(async () => (await gw.textareaInfo())?.tileID).toBe(visit);

  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;

  // Typed and not yet saved when the drop lands: the save is debounced, and
  // it is the row's by id, which the move keeps.
  await window.keyboard.press('End');
  await window.keyboard.type(' before');
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx + 2, cy), 'right');
  await kept(gw, scratch, home.gridID, visit, cx + 2, cy, n);
  const f = await gw.focused();
  expect([f.textFocus, f.gridID], 'the pane is still on the row, on its grid').toEqual([visit, home.gridID]);
  expect((await gw.textareaInfo())?.tileID, 'the raw editor is still on it').toBe(visit);
  await typesInto(window, 'editor');
  await window.keyboard.type(' after');
  await expect
    .poll(() => gw.getTileContent(visit), { timeout: 10_000 })
    .toBe('a text to visit before after');
  expect(await gw.textareaValue(), 'the editor shows it').toBe('a text to visit before after');
});

// A ctrl + right-drag of a visit's crumb links to it as to any tile (Joe,
// 2026-11-20: chose to allow the link, since a closed visit goes to the
// trash): the visit stays a visit, and once it closes the link leads to the
// same row in the trash.
test('a visit ctrl + right-dragged off its crumb is linked, and the link follows it to the trash', async ({ window, gw }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const tpl = await createText(origin, tg, 0, 0, 'a text to link');
  const { home, dest } = await twoPanes(gw);
  const scratch = await gw.scratchGridID('home');
  const cx = Math.round(dest.cx);
  const cy = Math.round(dest.cy);

  await visitTemplate(window, gw, tpl.id);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const visit = (await gw.focused()).textFocus;
  expect(await onGrid(gw, scratch, visit), 'the visit is on the scratch grid').toBeTruthy();
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;

  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, cx - 2, cy), 'right', true);
  await expect.poll(async () => ((await gw.getGrid(home.gridID)).tiles ?? []).length, { timeout: 15_000 }).toBe(n + 1);
  const link = ((await gw.getGrid(home.gridID)).tiles ?? []).find((t) => t.linkTargetId === visit)!;
  expect(link, 'the drop made a link to the visit').toBeTruthy();
  expect([Number(link.x ?? 0), Number(link.y ?? 0)], 'on the cell the drop resolved').toEqual([cx - 2, cy]);
  await stillAVisit(gw, scratch, visit);
  expect(await notices(window), 'nothing went wrong').toEqual([]);

  // Closing the visit sends its row to the trash, where the link still leads.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => (await trashed(origin, visit))?.id, { message: 'the closed visit is in the trash', timeout: 10_000 })
    .toBe(visit);
  expect((await onGrid(gw, home.gridID, link.id))?.linkTargetId, 'the link still names the same row').toBe(visit);
  await gw.waitIdle();
  const dead = await window.evaluate((gid: string) => (window as any).__gridwellTest.deadLinks(gid), home.gridID);
  expect(dead, 'the link is not dead').not.toContain(link.id);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test('a shell visit right-dragged off its crumb is kept on its tmux', async ({ window, gw }) => {
  const { home, dest } = await twoPanes(gw);
  const scratch = await gw.scratchGridID('home');
  await gw.clickMenuSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const visit = (await gw.focused()).textFocus;
  await gw.shellAttached();
  await window.keyboard.type('GW_KEPT=kept-on-its-tmux');
  await window.keyboard.press('Enter');
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;

  const dropX = Math.round(dest.cx) + 1;
  const dropY = Math.round(dest.cy) - 1;
  await dragCrumb(window, gw, visit, await gw.cellCenter(dest.id, dropX, dropY), 'right');
  await kept(gw, scratch, home.gridID, visit, dropX, dropY, n);
  const f = await gw.focused();
  expect([f.textFocus, f.gridID], 'the pane is still on the row, on its grid').toEqual([visit, home.gridID]);

  // The same shell: what it was told before the drop it still knows.
  await typesInto(window, 'terminal');
  await window.keyboard.type(`printf '%s\\n' "$GW_KEPT"`);
  await window.keyboard.press('Enter');
  await gw.waitShellLine('kept-on-its-tmux');
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test('a door visit right-dragged onto a grid is kept with its grid and what is on it, and the pane stays inside', async ({ window, gw }) => {
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
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;

  const dropX = Math.round(dest.cx) - 1;
  const dropY = Math.round(dest.cy);
  await dragCrumb(window, gw, visit.id, await gw.cellCenter(dest.id, dropX, dropY), 'right', false, visit.childGridId);
  const row = await kept(gw, scratch, home.gridID, visit.id, dropX, dropY, n);
  expect(row.childGridId, 'its own grid').toBe(visit.childGridId);
  expect(await onGrid(gw, visit.childGridId!, inside.id), 'what was put inside it is still there').toBeTruthy();
  const f = await gw.focused();
  expect([f.id, f.gridID], 'the same pane, still inside the door').toEqual([before.id, visit.childGridId]);
  expect([f.cx, f.cy, f.zoom], 'at the framing it had').toEqual([before.cx, before.cy, before.zoom]);

  // It is a tile like any other now: leaving retires nothing.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(home.gridID);
  await gw.waitIdle();
  expect((await onGrid(gw, home.gridID, visit.id))?.id, 'the ascent kept it').toBe(visit.id);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

// A move reads the row, never what a copy of it would become, so a visit
// whose copy would be a link moves as its left-drag from a grid would (Joe,
// 2026-11-20: "If I drag from a grid, from the menu, or from the nav, the
// answer should be the same.").
test('a link visit right-dragged off its crumb is kept: the same link, not refused', async ({ window, gw }) => {
  const origin = new URL(window.url()).origin;
  const tg = await templatesGrid(window);
  const { home, dest } = await twoPanes(gw);
  const cx = Math.round(dest.cx);
  const cy = Math.round(dest.cy);
  const target = await createText(origin, home.gridID, cx - 3, cy, 'what the link shows');
  const tpl = await createLeafLink(origin, tg, target.id, 'a link to visit', 0, 0);
  const scratch = await gw.scratchGridID('home');

  await visitTemplate(window, gw, tpl.id);
  await expect.poll(async () => ((await gw.getGrid(scratch)).tiles ?? []).length, { timeout: 15_000 }).toBe(1);
  const visit = ((await gw.getGrid(scratch)).tiles ?? [])[0];
  expect(visit.linkTargetId, 'the visit is a link to the text').toBe(target.id);
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
  const before = await gw.focused();
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;

  await dragCrumb(window, gw, visit.id, await gw.cellCenter(dest.id, cx + 2, cy), 'right');
  const row = await kept(gw, scratch, home.gridID, visit.id, cx + 2, cy, n);
  expect(row.linkTargetId, 'the same link').toBe(target.id);
  const f = await gw.focused();
  expect([f.id, f.textFocus, f.gridID], 'the pane is still on it, on its grid').toEqual([before.id, before.textFocus, home.gridID]);
  expect(await getTile(origin, target.id).then((t) => t.gridId), 'the text it shows stayed').toBe(home.gridID);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
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

// A workspace tile's visit drags from its boundary band: its panes are its
// own, so the only grids to drop it on are the ones they show; the level
// follows the row and leaving lands where it did.
test('a workspace visit right-dragged off its boundary crumb is kept with its layout', async ({ window, gw }) => {
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
  const n = ((await gw.getGrid(home.gridID)).tiles ?? []).length;

  const dropX = Math.round(dest.cx) + 1;
  const dropY = Math.round(dest.cy) + 1;
  const at = await gw.cellCenter(dest.id, dropX, dropY);
  await dragCrumb(window, gw, ws.id, at);
  expect(await onGrid(gw, home.gridID, ws.id), 'a left-drag moved nothing').toBeUndefined();
  expect(await depth(), 'and left nothing').toBe(1);

  await dragCrumb(window, gw, ws.id, at, 'right');
  await kept(gw, scratch, home.gridID, ws.id, dropX, dropY, n);
  expect((await getTile(origin, ws.id)).version, 'a move changes no layout').toBe(before.version);
  expect(await gw.getTileContent(ws.id), 'the layout still holds the text').toContain(text.id);
  expect(await depth(), 'still inside').toBe(1);
  expect((await gw.focused()).textFocus, 'the panes are as they were').toBe(text.id);

  // Leaving lands where it did, and retires nothing.
  await gw.leaveWorkspace();
  await expect.poll(depth).toBe(0);
  const back = await gw.focused();
  expect([back.id, back.gridID], 'back on the pane it was entered from').toEqual([home.id, home.gridID]);
  await gw.waitIdle();
  expect((await onGrid(gw, home.gridID, ws.id))?.id, 'the leave kept it').toBe(ws.id);
  expect((await gw.getGrid(scratch)).tiles ?? [], 'the scratch grid is empty').toEqual([]);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test.describe('across a connection', () => {
  test.use({ extraNodes: ['second'] });

  // There is no move across a connection, so a visit's crumb dropped on a
  // far grid is refused and says so, as information (Joe, 2026-11-19).
  test('a right-drag of a visit onto a far grid is refused as the move it is', async ({ window, gw }) => {
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

    await dragCrumb(window, gw, visit, await gw.cellCenter(far.id, Math.round(far.cx), Math.round(far.cy)), 'right');
    const told = async () =>
      ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).find(
        (x: any) => x.message === 'an ephemeral visit does not move across a connection',
      )?.severity ?? null;
    await expect.poll(told).toBe('info');
    expect(((await gw.getGrid(far.gridID)).tiles ?? []).length, 'nothing landed on the far grid').toBe(n);
    await stillAVisit(gw, scratch, visit);
  });
});
