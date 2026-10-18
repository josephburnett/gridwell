import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// Crosses the caps seam in client/caps: on a host with no Electron bridge a url
// tile can never go live, and every gesture that asks for a live view must
// explain itself with an Info notice rather than do nothing.

async function livecapMessage(window: any): Promise<string | null> {
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  return e.notices.find((n: any) => n.source === 'livecap')?.message ?? null;
}

test('an ephemeral url visit explains itself instead of opening a dead modal', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.clickMenuSwatch('url'); // opens the menu itself
  // No modal: the visit could only ever produce a blank frozen tile.
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

  // A url tile is a real persisted thing the desktop can visit later, and it
  // stays frozen here. The drop lands bare, the first descent prompts for the
  // address, and with no live capability the descent is the frozen one.
  const addr = `${serve.origin}/wasm_exec.js?newtab=1`;
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', addr);
  await window.locator('#gw-url-form').evaluate((form: HTMLFormElement) => form.requestSubmit());
  await gw.waitIdle();
  const t = tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)!;
  expect(t, 'url tile created (frozen) from a plain browser').toBeTruthy();
  const versionBefore = t.version;

  // A browser host cannot place a live view, so the circle button opens the
  // frozen address in a new tab. It arrives as the context 'page' event, because
  // noopener severs the popup relationship.
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  const menu = await gw.menu();
  const [popup] = await Promise.all([
    window.context().waitForEvent('page', { timeout: 10_000 }),
    window.mouse.click(menu.plusX, menu.plusY),
  ]);
  await popup.waitForURL(/newtab=1/, { timeout: 10_000 });
  expect(popup.url()).toBe(addr);
  await popup.close();

  const after = tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)!;
  expect(after.version, 'opening a tab must not touch the tile').toBe(versionBefore);
});
