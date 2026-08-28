import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #233: how you leave a text tile is how it presents. Ascending out
// of RENDERED mode must (1) persist text_mode on the tile, (2) switch the
// parent-grid preview to the rasterized rendered document (the
// foreignObject raster of markdown.RenderHTML — no second layout engine),
// and (3) restore rendered mode on the next descent. The raster state is
// read through the renderedPreviews testhook; the mode through GetGrid.

test('ascending in rendered mode persists it, renders the preview, and restores on descent', async ({
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

  // Descend (text mode by default), write a heading, flip to rendered.
  await gw.descendCell(cx, cy);
  await gw.typeText('# Big Heading\n\nbody text');
  await window.locator('#gw-text-toggle').click();
  await expect(window.locator('#gw-rendered-view')).toBeVisible();

  // Ascend: the mode is a durable tile fact...
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => {
      const t = (await gw.getGrid(grid)).tiles?.find((n: any) => n.id === created.id);
      return t?.textMode ?? '';
    }, { timeout: 10_000 })
    .toBe('rendered');

  // ...and the preview follows it: the raster decodes and draws.
  await expect
    .poll(
      async () =>
        window.evaluate(
          (id: string) => (window as any).__gridwellTest.renderedPreviews()[id] ?? null,
          created.id,
        ),
      { timeout: 10_000 },
    )
    .toMatchObject({ ready: true, failed: false });

  // Re-descending lands back in rendered mode — the overlay, not the textarea.
  await gw.descendCell(cx, cy);
  await expect(window.locator('#gw-rendered-view')).toBeVisible();
});
