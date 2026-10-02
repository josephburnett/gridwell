import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A text tile's letters keep their size at every moment of a grid zoom: the
// box is a window that reveals more as it grows (markdown.PreviewWindowFrame).
// A rendered preview is a raster made at a width bucket, so the spec zooms
// until the box crosses into a second bucket and reads, through the textFaces
// hook, the drawImage rects of each painted face: the drawn scale is the
// same at every zoom, no frame fell back to raw source on the way, and the
// tile's own row was never written.

const WRITES = ['PlaceTile', 'CloneTile', 'CreateTile', 'SetTile', 'DeleteTile', 'SetFraming', 'WriteContent'];

interface Face {
  kind: 'raster' | 'raw';
  scale?: number;
  rasterW?: number;
  dest?: number[];
  rawAfterRaster: number;
}

test('a rendered preview keeps its letter height across a grid zoom that crosses a raster bucket', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(grid), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();
  await gw.descendCell(cx, cy);
  await gw.typeText('# Heading\n\nbody text that wraps across the width of the tile');
  await gw.toggleTextMode();
  await expect(window.locator('#gw-rendered-view')).toBeVisible();
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => (await gw.getGrid(grid)).tiles?.find((t: any) => t.id === created.id)?.textMode ?? '', {
      timeout: 10_000,
    })
    .toBe('rendered');

  const face = (): Promise<Face | undefined> =>
    window.evaluate((id: string) => (window as any).__gridwellTest.textFaces()[id], created.id);
  await expect.poll(async () => (await face())?.kind, { timeout: 10_000 }).toBe('raster');

  // Every write from here on is the zoom's; the tile's row is the oracle that
  // it was never stamped (see the first-look rule, 4136066a).
  const c = await gw.cadences();
  await window.waitForTimeout(c.framingSaveMs * 2 + c.textSaveMs * 2);
  const rowBefore = (await gw.getGrid(grid)).tiles?.find((t: any) => t.id === created.id);
  const writes: { method: string; namesTile: boolean }[] = [];
  await window.route('**/gridwell.v1.Gridwell/*', async (r) => {
    const method = r.request().url().split('/').pop() ?? '';
    if (WRITES.includes(method)) {
      const body = r.request().postDataJSON() ?? {};
      writes.push({ method, namesTile: body.tileId === created.id || body.tile?.id === created.id });
    }
    await r.continue();
  });

  // Zoom in small steps until the box has crossed into a second bucket, and
  // past it, taking one sample per settled step.
  const samples: Face[] = [];
  const buckets = new Set<number>();
  for (let i = 0; i < 40 && (buckets.size < 2 || samples.length < 3); i++) {
    await gw.wheelAtFocusedCenter(-60);
    await expect.poll(async () => (await face())?.kind, { timeout: 10_000 }).toBe('raster');
    const s = (await face())!;
    samples.push(s);
    buckets.add(s.rasterW!);
  }
  expect(buckets.size, `the zoom crossed a raster bucket: ${JSON.stringify(samples)}`).toBeGreaterThanOrEqual(2);
  const scales = samples.map((s) => s.scale!);
  for (const s of scales) {
    expect(s, `drawn scale at every zoom: ${JSON.stringify(samples)}`).toBeCloseTo(scales[0], 6);
  }
  expect((await face())!.rawAfterRaster, 'a frame mid-zoom painted raw source').toBe(0);

  await window.waitForTimeout(c.framingSaveMs * 2);
  await window.unroute('**/gridwell.v1.Gridwell/*');
  const rowAfter = (await gw.getGrid(grid)).tiles?.find((t: any) => t.id === created.id);
  expect(rowAfter, 'the zoom wrote nothing to the tile').toEqual(rowBefore);
  expect(writes.filter((w) => w.namesTile || w.method !== 'SetFraming'), 'only the grid framing is written').toEqual([]);
});
