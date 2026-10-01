import { test, expect } from './fixtures';

// A url view whose renderer dies is gone, as one whose page closed itself is:
// main retires it, the renderer drops its handle, and the pane shows the tile's
// frozen face with one notice saying why. Descending again places a new live
// view, because a dead renderer's blank view has no Reload it could raise.

test('a crashed url view retires, and the next descent is live again', async ({ electronApp, gw, window }) => {
  const liveIn = (paneId: string) =>
    electronApp.evaluate((_e, id) => (globalThis as any).__gwRegistry.has(id) as boolean, paneId);
  const urlViews = () =>
    window.evaluate(() => (window as any).__gridwellTest.urlViews() as Record<string, string>);
  const notices = async () =>
    (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices as {
      severity: string;
      message: string;
    }[];

  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?crash=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await expect.poll(() => liveIn(home.id), { timeout: 15_000 }).toBe(true);
  await expect.poll(async () => Object.keys(await urlViews())).toContain(home.id);
  await gw.waitIdle();
  const before = (await notices()).length;

  await electronApp.evaluate((_e, id) => {
    (globalThis as any).__gwRegistry.webContentsFor(id).forcefullyCrashRenderer();
  }, home.id);

  await expect.poll(() => liveIn(home.id), { message: 'main retired the crashed view' }).toBe(false);
  await expect
    .poll(async () => Object.keys(await urlViews()), { message: 'the renderer dropped its handle' })
    .not.toContain(home.id);
  expect((await notices()).slice(before).map((n) => `${n.severity}: ${n.message}`)).toEqual([
    expect.stringMatching(/^error: page crashed .*crash=1/),
  ]);
  expect((await gw.focused()).textFocus, 'the pane stays in the tile, on its frozen face').not.toBe('');

  await gw.middleClickPane();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.waitIdle();
  await gw.descendCell(cx, cy);
  await expect.poll(() => liveIn(home.id), { timeout: 15_000, message: 'the next descent went live' }).toBe(true);
  await expect.poll(async () => Object.keys(await urlViews())).toContain(home.id);
  await gw.waitIdle();
  expect((await notices()).slice(before).map((n) => n.message)).toEqual([expect.stringMatching(/page crashed/)]);
});
