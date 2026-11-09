import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { createText, getTile, handshake, tileAt } from '../e2e/oracle';
import { longPressDrag } from './touch';

// The templates by finger (Joe, 2026-11-09): a template is a Gridwell tile,
// so a finger does everything a mouse does with one. Real TouchEvents through
// client/touchgest: the copy swatch dragged onto a text copies it in, a tap
// on the template visits an ephemeral copy, a one-finger drag moves it out, a
// long-press drag copies it, a drag to the trashcan deletes it, and a drag
// between the strip's items scrolls the strip.

type Pt = { x: number; y: number };

async function fingerDrag(page: Page, from: Pt, to: Pt) {
  const s = await page.context().newCDPSession(page);
  await s.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [from] });
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

const center = (i: { x: number; y: number; w: number; h: number }): Pt => ({ x: i.x + i.w / 2, y: i.y + i.h / 2 });

async function openStrip(gw: any, window: Page): Promise<void> {
  let menu = await gw.menu();
  if (!menu.open) {
    await window.touchscreen.tap(menu.plusX, menu.plusY);
    await window.waitForFunction(() => (window as any).__gridwellTest.menu().open, null, { timeout: 5_000 });
    menu = await gw.menu();
  }
  if (!menu.toggle.expanded) {
    await window.touchscreen.tap(menu.toggle.x + menu.toggle.w / 2, menu.toggle.y + menu.toggle.h / 2);
    await window.waitForFunction(() => (window as any).__gridwellTest.menu().toggle.expanded, null, { timeout: 5_000 });
  }
}

async function stripItem(gw: any, pred: (i: any) => boolean, what: string): Promise<any> {
  let found: any;
  await expect
    .poll(async () => {
      found = (await gw.menu()).items.find(pred);
      return !!found;
    }, { message: `the strip shows ${what}`, timeout: 10_000 })
    .toBe(true);
  return found;
}

test('touch: a finger makes, visits, moves, copies and deletes a template', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  const tg = (await handshake(origin)).templatesGridId!;
  expect(tg, 'the handshake names the templates grid').toBeTruthy();
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  const text = await createText(origin, home.gridID, cx, cy, 'by finger');
  await gw.waitClientTileAt(home.id, cx, cy, text.id);

  // The copy swatch dragged onto the text copies it in.
  await openStrip(gw, window);
  const plus = await stripItem(gw, (i) => i.copySwatch, 'the copy swatch');
  await fingerDrag(window, center(plus), await gw.cellCenter(home.id, cx, cy));
  await gw.waitIdle();
  await expect.poll(async () => ((await gw.getGrid(tg)).tiles ?? []).length, { message: 'the text was copied in' }).toBe(1);
  const tpl = ((await gw.getGrid(tg)).tiles ?? [])[0];
  const before = await getTile(origin, tpl.id);

  // A tap visits an ephemeral copy; the template stays as it was.
  let item = await stripItem(gw, (i) => i.template === tpl.id, 'the template');
  await window.touchscreen.tap(item.x + item.w / 2, item.y + item.h / 2);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  expect((await gw.focused()).textFocus, 'the visit is a copy').not.toBe(tpl.id);
  await gw.ascendViaCrumb();
  expect(await getTile(origin, tpl.id), 'the template is as it was').toEqual(before);

  // A long-press drag copies it out.
  await openStrip(gw, window);
  item = await stripItem(gw, (i) => i.template === tpl.id, 'the template');
  await longPressDrag(window, center(item), await gw.cellCenter(home.id, cx + 1, cy));
  await gw.waitIdle();
  await expect.poll(async () => tileAt(await gw.getGrid(home.gridID), 'text', cx + 1, cy)?.id ?? '', { message: 'the long-press drag copied it' }).not.toBe('');
  expect(((await gw.getGrid(tg)).tiles ?? []).some((t) => t.id === tpl.id), 'the template stayed').toBe(true);

  // A one-finger drag moves it out.
  await openStrip(gw, window);
  item = await stripItem(gw, (i) => i.template === tpl.id, 'the template');
  await fingerDrag(window, center(item), await gw.cellCenter(home.id, cx + 2, cy));
  await gw.waitIdle();
  await expect.poll(async () => tileAt(await gw.getGrid(home.gridID), 'text', cx + 2, cy)?.id, { message: 'the drag moved it' }).toBe(tpl.id);

  // A template dragged to the trashcan is deleted.
  const doomed = await createText(origin, tg, 0, 0, 'doomed');
  await openStrip(gw, window);
  item = await stripItem(gw, (i) => i.template === doomed.id, 'the template to delete');
  const menu = await gw.menu();
  await fingerDrag(window, center(item), { x: menu.plusX, y: menu.plusY });
  await gw.waitIdle();
  await expect.poll(async () => ((await gw.getGrid(tg)).tiles ?? []).length, { message: 'the trashcan deleted it' }).toBe(0);
});

test('touch: a drag between the strip items scrolls the strip', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  const tg = (await handshake(origin)).templatesGridId!;
  for (let i = 0; i < 14; i++) await createText(origin, tg, 0, -i, `t${i}`);
  await gw.sources();
  await openStrip(gw, window);
  await expect.poll(async () => (await gw.menu()).items.filter((i: any) => i.template).length).toBe(14);
  const menu = await gw.menu();
  const a = menu.items.find((i: any) => i.copySwatch)!;
  // The gutter beside the first item, inside the strip's band.
  const from = { x: a.x - menu.strip.x > 4 ? a.x - 3 : a.x + a.w + 3, y: a.y + a.h / 2 };
  await fingerDrag(window, from, { x: from.x, y: from.y - 60 });
  await expect.poll(async () => (await gw.menu()).strip.scroll, { message: 'the drag scrolled the strip' }).toBeGreaterThan(0);
  expect((await gw.menu()).open, 'and the menu stayed open').toBe(true);
});
