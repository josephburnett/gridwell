import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from '../e2e/homes';
import { tileAt } from '../e2e/oracle';

// A finger does not move or delete a file (CLAUDE.md, 2026-11-08): real
// TouchEvents through client/touchgest reach dragdrop.DecideDrop with the
// press's Touch set, so a one-finger drag of a file onto another directory or
// the trashcan snaps back with the reason, nothing asked of the node, while
// placing it in its own grid lands and a Gridwell tile on the same grid is
// deleted as with a mouse.

const ROOT = makeRunDir();
fs.mkdirSync(path.join(ROOT, 'sub'), { recursive: true });
fs.writeFileSync(path.join(ROOT, 'notes.md'), '# notes\n');
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

const REASON = 'a finger does not move or delete a file; use a mouse';

async function fingerDrag(page: Page, from: { x: number; y: number }, to: { x: number; y: number }) {
  const s = await page.context().newCDPSession(page);
  await s.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: from.x, y: from.y }] });
  const steps = 8;
  for (let i = 1; i <= steps; i++) {
    await s.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: [{ x: from.x + ((to.x - from.x) * i) / steps, y: from.y + ((to.y - from.y) * i) / steps }],
    });
  }
  await s.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await s.detach();
}

test('touch: a finger does not move or delete a file, and does a Gridwell tile', async ({ gw, window }) => {
  const asked: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/{MoveTile,DeleteTile}', async (r: any) => {
    asked.push(r.request().url().split('/').pop());
    await r.continue().catch(() => {});
  });
  const notices = async () =>
    ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []) as { message: string }[];

  await gw.enterSource('docs (files)');
  const f = await gw.focused();
  const tiles = (await gw.getGrid(f.gridID)).tiles ?? [];
  const sub = tiles.find((t: any) => t.altText === 'sub')!;
  const notes = tiles.find((t: any) => t.altText === 'notes.md')!;
  expect(sub && notes, 'the directory and the file are listed').toBeTruthy();
  const [nx, ny] = [Number(notes.x ?? 0), Number(notes.y ?? 0)];
  const at = (x: number, y: number) => gw.cellCenter(f.id, x, y);

  // Onto the subdirectory's door: a rename across directories.
  await fingerDrag(window, await at(nx, ny), await at(Number(sub.x ?? 0), Number(sub.y ?? 0)));
  await gw.waitIdle();
  expect(asked, 'nothing was asked of the node').toEqual([]);
  expect(fs.existsSync(path.join(ROOT, 'notes.md')), 'the file stayed').toBe(true);
  await expect.poll(async () => (await notices()).some((n) => n.message.includes(REASON)), { message: 'the reason is told' }).toBe(true);

  // Onto the trashcan: a delete.
  const menu = await gw.menu();
  await fingerDrag(window, await at(nx, ny), { x: menu.plusX, y: menu.plusY });
  await gw.waitIdle();
  expect(asked, 'nothing was asked of the node').toEqual([]);
  expect(fs.existsSync(path.join(ROOT, 'notes.md')), 'the file is not in the trash').toBe(true);

  // Within its own grid a finger places it.
  const taken = (x: number, y: number) => (tiles as any[]).some((t) => Number(t.x ?? 0) === x && Number(t.y ?? 0) === y);
  let [px, py] = [nx + 1, ny];
  while (taken(px, py)) px++;
  await fingerDrag(window, await at(nx, ny), await at(px, py));
  await gw.waitIdle();
  expect(asked, 'the placement was a move').toEqual(['MoveTile']);
  await expect.poll(async () => tileAt(await gw.getGrid(f.gridID), 'text', px, py)?.id, { message: 'the file sits where the finger left it' }).toBe(notes.id);

  // A Gridwell text on the same grid goes to the trash by a finger.
  let [tx, ty] = [px + 1, py];
  while (taken(tx, ty)) tx++;
  await gw.openMenu();
  await gw.dragCreate('markdown', tx, ty);
  const text = tileAt(await gw.getGrid(f.gridID), 'text', tx, ty)!;
  expect(text && !text.id!.includes('~'), 'a Gridwell text on the directory').toBeTruthy();
  asked.length = 0;
  await fingerDrag(window, await at(tx, ty), { x: menu.plusX, y: menu.plusY });
  await gw.waitIdle();
  expect(asked, 'the Gridwell tile was deleted').toEqual(['DeleteTile']);
  await expect.poll(async () => tileAt(await gw.getGrid(f.gridID), 'text', tx, ty), { message: 'and is gone from the directory' }).toBeFalsy();
});
