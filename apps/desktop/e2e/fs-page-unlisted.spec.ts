import * as fs from 'node:fs';
import * as path from 'node:path';
import { test, expect } from './fixtures';
import { createLeafLink, tileAt } from './oracle';
import { makeRunDir } from './homes';

// A page the node serves has no address of its own, so a click on one never
// asks the user for an address. A directory the OS refuses this user is a
// verdict, not darkness: its door stays listed and says why once a read of
// it was refused, a descent is refused with the reason, and the files stay
// light; given back, it opens and its page descends. A file that is gone is
// a gone path, so a link to it is dead, and a dead link asks nothing either.
// A dark directory, one that will not read for a while, has no case here:
// e2e can fail a read only by permission, and that is the refusal.

// One 1x1 png: an image is a page the node serves.
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==',
  'base64',
);

const modal = (window: any) => window.locator('#gw-url-modal.open');

async function notice(window: any, source: string): Promise<string> {
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  return String((errs.notices ?? []).find((n: any) => n.source === source)?.message ?? '');
}

test.describe('a page whose directory is refused', () => {
  const siteDir = makeRunDir();
  const pics = path.join(siteDir, 'pics');
  fs.mkdirSync(pics);
  fs.writeFileSync(path.join(pics, 'kept.png'), PNG);
  test.use({ filesRoots: [{ root: siteDir, label: 'site' }] });

  test('its door says why, a descent is refused, and given back the page opens', async ({ gw, window }) => {
    fs.chmodSync(pics, 0o000);
    let picsGrid = '';
    try {
      await gw.enterSource('site (files)');
      const f = await gw.focused();
      const door = async () => (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'pics');
      const d = (await door())!;
      expect(d?.kind, 'the refused directory is still listed').toBe('door');
      picsGrid = d.childGridId!;
      const source = 'grid:' + picsGrid;
      // The door's preview is the first read of it, and its refusal is said on
      // the door, not on the notice row.
      await expect
        .poll(async () => String((await door())?.readOnly ?? ''), { message: 'the door says why', timeout: 15_000 })
        .toContain('cannot be read');
      expect(await notice(window, source), 'a preview raises no notice').toBe('');
      expect((await gw.focused()).cached, 'the files are not dark').toBeFalsy();

      await gw.descendCell(Number(d.x ?? 0), Number(d.y ?? 0));
      await expect
        .poll(() => notice(window, source), { message: 'the descent is refused with the reason', timeout: 15_000 })
        .toContain('cannot be read');
      expect((await gw.focused()).cached, 'a refusal is not dark').toBeFalsy();
      await gw.ascendViaCrumb();
      await expect.poll(async () => (await gw.focused()).gridID).toBe(f.gridID);
    } finally {
      fs.chmodSync(pics, 0o755);
    }

    await expect
      .poll(async () => (await gw.getGrid(picsGrid)).tiles!.map((t) => t.altText), {
        message: 'given back, the next read lists it',
        timeout: 15_000,
      })
      .toEqual(['kept.png']);
    const f = await gw.focused();
    const d = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'pics')!;
    await gw.descendCell(Number(d.x ?? 0), Number(d.y ?? 0));
    await expect.poll(async () => (await gw.focused()).gridID, { timeout: 15_000 }).toBe(picsGrid);
    await expect
      .poll(() => notice(window, 'grid:' + picsGrid), { message: 'the descent that lists it clears the notice', timeout: 15_000 })
      .toBe('');

    const page = (await gw.getGrid(picsGrid)).tiles!.find((t) => t.altText === 'kept.png')!;
    expect(page.servesPage, 'an image is a page the node serves').toBe(true);
    await gw.descendCell(Number(page.x ?? 0), Number(page.y ?? 0));
    await expect
      .poll(async () => (await gw.focused()).textFocus, { message: 'the click descends into the page', timeout: 15_000 })
      .toBe(page.id);
    await expect(modal(window), 'and asks for no address').toHaveCount(0);
  });
});

test.describe('a page whose file is gone', () => {
  const siteDir = makeRunDir();
  test.use({ filesRoots: [{ root: siteDir, label: 'site' }] });

  test('a link to it greys, and a click asks nothing', async ({ gw, window }) => {
    const file = path.join(siteDir, 'gone.png');
    fs.writeFileSync(file, PNG);
    const grid = await gw.swatchGridID('site (files)');
    const entry = ((await gw.getGrid(grid)).tiles ?? []).find((t) => t.altText === 'gone.png');
    expect(entry?.servesPage, 'the image is listed as a page').toBe(true);
    fs.rmSync(file);

    await gw.enterSource('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx) + 1;
    const cy = Math.round(f.cy) + 1;
    await createLeafLink(gw.origin, f.gridID, entry!.id!, 'gone.png', cx, cy, 'url');
    await expect.poll(async () => tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)?.link).toBe(true);

    // A url link shows no preview of its own, so the click is the first read of
    // its target, and that read is what hears the target is gone.
    const before = await gw.focused();
    await gw.descendCell(cx, cy);
    await expect
      .poll(
        () => window.evaluate((gid: string) => (window as any).__gridwellTest.deadLinks(gid), f.gridID),
        { message: 'the link to a gone page is dead', timeout: 15_000 },
      )
      .toHaveLength(1);
    await expect(modal(window), 'a dead page asks for no address').toHaveCount(0);
    await expect
      .poll(async () => (await gw.focused()).placeDepth, { message: 'the pane leaves the dead link', timeout: 15_000 })
      .toBe(before.placeDepth);
    expect((await gw.focused()).textFocus, 'and opens nothing').toBe('');
    const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
    expect(errs.notices, 'a dead link says nothing').toEqual([]);
    expect(tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)?.altText, 'still labelled').toBe('gone.png');

    // Now that it is known dead, a click is no door at all.
    await gw.descendCell(cx, cy);
    expect((await gw.focused()).placeDepth, 'a dead link is not a door').toBe(before.placeDepth);
    await expect(modal(window)).toHaveCount(0);
  });
});
