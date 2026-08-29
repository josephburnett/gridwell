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
  await gw.enterPlugin('home');
  await gw.clickPaletteSwatch('url'); // opens the palette itself
  // No modal — the visit could only ever produce a blank frozen tile.
  await expect(window.locator('#gw-url-modal.open')).toHaveCount(0);
  await expect.poll(async () => livecapMessage(window)).toContain('desktop');
});

test('a frozen url tile: the circle button opens the address in a new tab', async ({
  gw,
  window,
  serve,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Drag-create still works in a browser: a url tile is a real, useful,
  // persisted thing (the desktop can visit it later); it just stays frozen.
  // The drop lands it bare (#209); the first descent prompts for the
  // address and, with no live capability, lands on the frozen descent.
  const addr = `${serve.origin}/wasm_exec.js?newtab=1`;
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', addr);
  await window.locator('#gw-url-form').evaluate((form: HTMLFormElement) => form.requestSubmit());
  await gw.waitIdle();
  const t = tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)!;
  expect(t, 'url tile created (frozen) from a plain browser').toBeTruthy();
  const versionBefore = t.version;

  // The submit descended; the circle is the open-in-new-tab affordance
  // (owner decision 2026-08-09): a browser host can't place a live view,
  // so the click opens the frozen address in a NEW TAB — the context
  // 'page' event, since noopener severs the popup relationship.
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  const pal = await gw.palette();
  const [popup] = await Promise.all([
    window.context().waitForEvent('page', { timeout: 10_000 }),
    window.mouse.click(pal.plusX, pal.plusY),
  ]);
  await popup.waitForURL(/newtab=1/, { timeout: 10_000 });
  expect(popup.url()).toBe(addr);
  await popup.close();

  // The gesture persisted NOTHING: the tile row is byte-for-byte as it
  // was — still frozen, same version.
  const after = tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)!;
  expect(after.version, 'opening a tab must not touch the tile').toBe(versionBefore);
});
