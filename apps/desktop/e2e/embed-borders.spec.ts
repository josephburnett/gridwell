import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #169: EVERY embed in a markdown doc is a link (deleting the markdown
// never deletes the target), and dashed-border is the established link
// indication. The dashed flag was already computed (embedIsLink) and threaded
// into drawNodeWithPreview — but the url/shell/pane/generic drawers dropped
// it and always stroked solid, so exactly the most common embed target (a
// url tile) lied about ownership. This spec renders a url embed and
// pixel-samples its border row: a dashed stroke has gaps (color transitions
// along the row); a solid stroke has none. Fails before the fix.

async function embedHits(
  window: any,
): Promise<Array<{ tileId: string; x: number; y: number; w: number; h: number }>> {
  return window.evaluate(() => (window as any).__gridwellTest.embedHits());
}

test('a url embed strokes a dashed (link) border', async ({ gw, window, electronApp }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) - 1;

  // A placed url tile (the embed target): dropped bare (#209), addressed at
  // the first descent.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.openPalette();
  await gw.dragCreate('url', cx + 1, cy);
  await gw.descendCell(cx + 1, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?embedborder=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  const target = tileAt(await gw.getGrid(f.gridID), 'url', cx + 1, cy)!;
  expect(target, 'url tile persisted').toBeTruthy();
  await gw.rightClickPlus(); // ascend out of the live url descent
  await gw.waitIdle();

  // A doc embedding it, in rendered mode.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.typeText(`see [u](/${target.id})`);
  await gw.toggleTextMode();
  await gw.waitIdle();

  const hits = await embedHits(window);
  expect(hits.length, 'one embed rendered').toBe(1);
  expect(hits[0].tileId).toBe(target.id);

  // drawEmbedAt centers a square of side min(w,h) in the hit rect; the tile
  // border strokes 2px just inside it. Sample the top border row and count
  // transitions between border-colored (#7a5a9a) and not.
  const h = hits[0];
  const side = Math.min(h.w, h.h);
  const sx = h.x + (h.w - side) / 2;
  const sy = h.y + (h.h - side) / 2;
  const probe = await window.evaluate(
    ([px, py, len]: number[]) => {
      const canvas = document.querySelector('canvas') as HTMLCanvasElement;
      const ctx = canvas.getContext('2d')!;
      const dpr = window.devicePixelRatio || 1;
      const row = ctx.getImageData(
        Math.round(px * dpr),
        Math.round(py * dpr),
        Math.max(1, Math.round(len * dpr)),
        1,
      ).data;
      const isBorder = (i: number) =>
        Math.abs(row[i] - 0x7a) < 40 && Math.abs(row[i + 1] - 0x5a) < 40 && Math.abs(row[i + 2] - 0x9a) < 40;
      let transitions = 0;
      let borderPx = 0;
      let prev = isBorder(0);
      if (prev) borderPx++;
      for (let i = 4; i < row.length; i += 4) {
        const cur = isBorder(i);
        if (cur !== prev) transitions++;
        if (cur) borderPx++;
        prev = cur;
      }
      return { transitions, borderPx, sampled: row.length / 4 };
    },
    [sx + 4, sy + 1, side - 8],
  );
  expect(probe.borderPx, 'the border row must actually be stroked').toBeGreaterThan(5);
  expect(
    probe.transitions,
    'a link embed must stroke DASHED — gaps along the border row',
  ).toBeGreaterThan(3);
});
