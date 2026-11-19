import type { Page } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { spawnSync } from 'node:child_process';
import { test, expect } from './fixtures';
import type { GridwellDriver, PaneInfo } from './driver';
import { homeEnv } from './homes';
import { createText, createTile, getTileContent, tileAt } from './oracle';

// Every crumb standing for a tile right-drags as its tile (Joe, 2026-11-18:
// "It should right-click out to make a copy (or paste into a shell or text
// file). Exactly the same as ephemeral tiles."): onto a shell it pastes the
// tile's reference, onto a grid it copies, with ctrl it links, and the pane
// the crumb rides keeps showing the tile. A left-drag of any crumb is its
// click (Joe, 2026-11-19), and the click still ascends.

// What the node's private tmux shows of a shell, asked the way the node asks.
function paneText(home: string, key: string): string {
  const id = /^id:\s*"?([^"\s]+)"?/m.exec(fs.readFileSync(path.join(home, 'server.yaml'), 'utf-8'))?.[1];
  const name = 'gridwell-' + Buffer.from(key).toString('base64url');
  const r = spawnSync('tmux', ['-L', `gridwell-${id}`, 'capture-pane', '-p', '-J', '-t', name], {
    env: { ...process.env, ...homeEnv(home) },
    encoding: 'utf-8',
  });
  return r.status === 0 ? r.stdout : '';
}

const notices = async (window: Page): Promise<string[]> =>
  ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).map((n: any) => String(n.message));

// The press on tile's crumb in the bar, one move and the release, in one
// synchronous turn, as visit-crumb-drag.spec.ts does and for its reason: a
// live view under the real cursor would otherwise end the drag early.
async function dragCrumb(window: Page, gw: GridwellDriver, tileID: string, to: { x: number; y: number }, button: 'left' | 'right', ctrl = false) {
  const bar = await gw.bar();
  const crumb = bar.segments.find((s) => s.tileID === tileID);
  expect(crumb, `the bar has the crumb of ${tileID}`).toBeTruthy();
  const from = { x: crumb!.x + crumb!.w / 2, y: bar.top + bar.height / 2 };
  await window.evaluate(
    ([fx, fy, tx, ty, b, c]: number[]) => {
      const canvas = document.querySelector('canvas')!;
      const fire = (type: string, x: number, y: number, buttons: number) =>
        canvas.dispatchEvent(new MouseEvent(type, { clientX: x, clientY: y, buttons, button: b, ctrlKey: c === 1, bubbles: true }));
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

const pane = async (gw: GridwellDriver, id: string): Promise<PaneInfo> => (await gw.panes()).find((p) => p.id === id)!;

test("a text's crumb right-dropped on a live shell pastes its reference and the pane keeps the text", async ({ gw, home, window }) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const body = 'a text to point at\n';
  const text = await createText(origin, grid, cx, cy, body);
  const shell = await createTile(origin, grid, 'shell', cx + 1, cy);
  await gw.waitClientTileAt(f.id, cx + 1, cy, shell.id);

  // B on the shell, live, with `echo ` typed; A on the text.
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const aId = (await gw.panes()).find((p) => p.id !== bId)!.id;
  await gw.descendCell(cx + 1, cy);
  await gw.shellAttached();
  await window.keyboard.type('echo ');
  await gw.focusPane(await pane(gw, aId));
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).toBe(text.id);

  const b = await pane(gw, bId);
  const key = shell.id.split('/').pop()!;
  const ref = `${origin}/${grid}/${text.id.split('/').pop()}`;

  // A left-drag of the current crumb is its click, which ascends nowhere.
  await dragCrumb(window, gw, text.id, { x: b.x + b.w / 2, y: b.y + b.h / 2 }, 'left');
  expect((await pane(gw, aId)).textFocus, 'the left-drag left the pane on the text').toBe(text.id);
  expect(paneText(home, key), 'the left-drag pasted nothing').not.toContain(ref);

  await dragCrumb(window, gw, text.id, { x: b.x + b.w / 2, y: b.y + b.h / 2 }, 'right');
  await expect
    .poll(() => paneText(home, key).includes(`echo ${ref}`), { message: `the reference ${ref} was pasted`, timeout: 10_000 })
    .toBe(true);
  const a = await pane(gw, aId);
  expect([a.textFocus, a.gridID], 'the pane still shows the text').toEqual([text.id, grid]);
  expect(await getTileContent(origin, text.id), 'the text is as it was').toBe(body);
  expect(tileAt(await gw.getGrid(grid), 'text', cx, cy)?.id, 'the text did not move').toBe(text.id);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});

test("a door's crumb right-dragged onto a grid copies the door, with ctrl links it, and its click still ascends", async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const door = await createTile(origin, grid, 'door', cx, cy);
  await gw.waitClientTileAt(f.id, cx, cy, door.id);

  // A inside the door, on a text there, so the door's crumb is not the current one.
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const aId = (await gw.panes()).find((p) => p.id !== bId)!.id;
  await gw.focusPane(await pane(gw, aId));
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 15_000 }).toBe(door.childGridId);
  const inner = await gw.focused();
  const ix = Math.round(inner.cx);
  const iy = Math.round(inner.cy);
  const text = await createText(origin, door.childGridId!, ix, iy, 'inside the door');
  await gw.waitClientTileAt(aId, ix, iy, text.id);
  await gw.descendCell(ix, iy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).toBe(text.id);

  const b = await pane(gw, bId);
  const bx = Math.round(b.cx);
  const by = Math.round(b.cy);
  const n = ((await gw.getGrid(grid)).tiles ?? []).length;

  // A right-drag copies: a door of its own, with a grid of its own.
  await dragCrumb(window, gw, door.id, await gw.cellCenter(bId, bx, by + 2), 'right');
  await expect.poll(async () => tileAt(await gw.getGrid(grid), 'door', bx, by + 2)?.id, { timeout: 15_000 }).toBeTruthy();
  const copy = tileAt(await gw.getGrid(grid), 'door', bx, by + 2)!;
  expect(copy.id, 'a new row').not.toBe(door.id);
  expect(copy.link, 'a copy, not a link').toBeFalsy();
  expect(copy.childGridId, 'a grid of its own').not.toBe(door.childGridId);

  // Ctrl + right-drag links to the door's grid.
  await dragCrumb(window, gw, door.id, await gw.cellCenter(bId, bx, by - 2), 'right', true);
  await expect.poll(async () => tileAt(await gw.getGrid(grid), 'door', bx, by - 2)?.id, { timeout: 15_000 }).toBeTruthy();
  const link = tileAt(await gw.getGrid(grid), 'door', bx, by - 2)!;
  expect(link.link, 'a link').toBeTruthy();
  expect(link.childGridId, "the door's own grid").toBe(door.childGridId);
  expect(((await gw.getGrid(grid)).tiles ?? []).length, 'two rows, nothing else').toBe(n + 2);

  // Over the trashcan it is refused and says why: nothing is deleted from the bar.
  expect(await notices(window), 'nothing went wrong').toEqual([]);
  await dragCrumb(window, gw, door.id, await gw.circleCenter(), 'right');
  await expect.poll(() => notices(window)).toEqual(["a crumb's drag copies; it does not delete"]);
  expect(((await gw.getGrid(grid)).tiles ?? []).length, 'nothing deleted').toBe(n + 2);

  // The pane kept the text, the door did not move, and the click ascends.
  const a = await pane(gw, aId);
  expect(a.textFocus, 'the pane still shows the text').toBe(text.id);
  expect(tileAt(await gw.getGrid(grid), 'door', cx, cy)?.id, 'the door did not move').toBe(door.id);
  const bar = await gw.bar();
  const crumb = bar.segments.find((s) => s.tileID === door.id)!;
  await window.mouse.click(crumb.x + crumb.w / 2, bar.top + bar.height / 2);
  await gw.waitIdle();
  await expect.poll(async () => (await pane(gw, aId)).gridID).toBe(door.childGridId);
  expect((await pane(gw, aId)).textFocus, 'the click ascended out of the text').toBe('');
  expect(await notices(window), 'nothing else was said').toEqual(["a crumb's drag copies; it does not delete"]);
});

// Every level's band right-drags as its workspace tile, an outer one's too
// (Joe, 2026-11-19: "Yes, the workspace tiles (bands) should right drag too.
// Tiles are tile."): the drag reads the row, not the tree the level parks.
test("an outer level's band right-dropped on a shell pastes its reference, and onto a grid copies its workspace tile", async ({ gw, home, window }) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const outer = await createTile(origin, grid, 'workspace', cx, cy);
  const inner = await createTile(origin, grid, 'workspace', cx + 1, cy);
  const shell = await createTile(origin, grid, 'shell', cx, cy + 1);
  await gw.waitClientTileAt(f.id, cx, cy + 1, shell.id);
  const depth = async () => (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;

  // Two levels deep: into the outer workspace tile, then the inner one.
  await gw.descendCell(cx, cy);
  await expect.poll(depth, { timeout: 15_000 }).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(grid);
  await gw.waitClientTileAt((await gw.focused()).id, cx + 1, cy, inner.id);
  await gw.descendCell(cx + 1, cy);
  await expect.poll(depth, { timeout: 15_000 }).toBe(2);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(grid);

  // B on the shell, live, with `echo ` typed; A on the grid.
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const aId = (await gw.panes()).find((p) => p.id !== bId)!.id;
  await gw.descendCell(cx, cy + 1);
  await gw.shellAttached();
  await window.keyboard.type('echo ');
  await gw.focusPane(await pane(gw, aId));
  const segs = (await gw.bar()).segments.filter((s) => s.kind === 'workspace').map((s) => [s.level, s.tileID]);
  expect(segs, 'the bar has both bands').toEqual([
    [1, outer.id],
    [2, inner.id],
  ]);

  const b = await pane(gw, bId);
  const key = shell.id.split('/').pop()!;
  const own = outer.id.split('/').pop()!;
  await dragCrumb(window, gw, outer.id, { x: b.x + b.w / 2, y: b.y + b.h / 2 }, 'right');
  await expect
    .poll(() => new RegExp(`echo '?${origin.replace(/[.]/g, '\\.')}/\\S*${own}`).test(paneText(home, key)), { message: `a reference to ${own} was pasted`, timeout: 10_000 })
    .toBe(true);
  expect(await depth(), 'still two levels deep').toBe(2);

  const a = await pane(gw, aId);
  const ax = Math.round(a.cx);
  const ay = Math.round(a.cy) + 2;
  const n = ((await gw.getGrid(grid)).tiles ?? []).length;
  await dragCrumb(window, gw, outer.id, await gw.cellCenter(aId, ax, ay), 'right');
  await expect.poll(async () => tileAt(await gw.getGrid(grid), 'workspace', ax, ay)?.id, { timeout: 15_000 }).toBeTruthy();
  const copy = tileAt(await gw.getGrid(grid), 'workspace', ax, ay)!;
  expect(copy.id, 'a new row').not.toBe(outer.id);
  expect(copy.link, 'a copy, not a link').toBeFalsy();
  expect(((await gw.getGrid(grid)).tiles ?? []).length, 'one row, nothing else').toBe(n + 1);
  expect(tileAt(await gw.getGrid(grid), 'workspace', cx, cy)?.id, 'the workspace tile did not move').toBe(outer.id);
  expect(await depth(), 'still two levels deep').toBe(2);
  expect(await notices(window), 'nothing went wrong').toEqual([]);
});
