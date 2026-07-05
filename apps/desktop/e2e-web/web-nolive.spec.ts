import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// Crosses the caps seam (client/caps): on a host with no Electron bridge a
// URL tile can never go live, and every gesture that asks for a live view
// must EXPLAIN itself (an Info notice) rather than silently do nothing —
// the launcher dead-click lesson applied to browser mode.

async function livecapMessage(window: any): Promise<string | null> {
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  return e.notices.find((n: any) => n.source === 'livecap')?.message ?? null;
}

test('an ephemeral url visit explains itself instead of opening a dead modal', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('e2e');
  await gw.clickPaletteSwatch('url'); // opens the palette itself
  // No modal — the visit could only ever produce a blank frozen tile.
  await expect(window.locator('#gw-url-modal.open')).toHaveCount(0);
  await expect.poll(async () => livecapMessage(window)).toContain('desktop');
});

test('a frozen url tile shows the no-live corner button and it explains on tap', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Drag-create still works in a browser: a url tile is a real, useful,
  // persisted thing (the desktop can visit it later); it just stays frozen.
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/');
  await window.locator('#gw-url-form').evaluate((form: HTMLFormElement) => form.requestSubmit());
  await gw.waitIdle();
  const t = tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)!;
  expect(t, 'url tile created (frozen) from a plain browser').toBeTruthy();

  // Descend; the corner button is the slashed no-live affordance. Tapping it
  // must post the explanatory notice, not silently no-op (and certainly not
  // try to place a native view).
  await gw.descendCell(cx, cy);
  const pal = await gw.palette();
  await window.mouse.click(pal.plusX, pal.plusY);
  await expect.poll(async () => livecapMessage(window)).toContain('desktop');
});
