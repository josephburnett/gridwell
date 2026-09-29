import { test, expect } from './fixtures';

// A page that calls window.close() (a sign-in flow's last page does) destroys
// its webContents under the registry. The pane must drop back to the tile's
// frozen face with one notice saying why, not sit blank behind a view that is
// gone, and leaving it afterwards must say nothing more.

test('a page that closes itself ends the live view with one notice', async ({ electronApp, gw, window }) => {
  const liveIn = (paneId: string) =>
    electronApp.evaluate((_e, id) => (globalThis as any).__gwRegistry.has(id) as boolean, paneId);
  const urlViews = () =>
    window.evaluate(() => (window as any).__gridwellTest.urlViews() as Record<string, string>);
  const notices = async () =>
    (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices as {
      source: string;
      message: string;
      severity: string;
    }[];

  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?selfclose=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await expect.poll(() => liveIn(home.id), { timeout: 15_000 }).toBe(true);
  await expect.poll(async () => Object.keys(await urlViews())).toContain(home.id);
  await gw.waitIdle();
  const before = (await notices()).length;

  // Not awaited: the page is gone before the script could answer.
  await electronApp.evaluate((_e, id) => {
    (globalThis as any).__gwRegistry.webContentsFor(id).executeJavaScript('window.close()').catch(() => {});
  }, home.id);

  await expect.poll(() => liveIn(home.id), { message: 'main retired the entry' }).toBe(false);
  await expect
    .poll(async () => Object.keys(await urlViews()), { message: 'the renderer dropped its handle' })
    .not.toContain(home.id);
  const said = (await notices()).slice(before);
  expect(said.map((n) => `${n.severity}: ${n.message}`)).toEqual([
    expect.stringMatching(/^info: the page closed itself/),
  ]);
  expect((await gw.focused()).textFocus, 'the pane stays in the tile, on its frozen face').not.toBe('');

  // Leaving has nothing left to close, so it raises nothing.
  await gw.middleClickPane();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.waitIdle();
  expect((await notices()).slice(before).map((n) => n.message)).toEqual([
    expect.stringMatching(/the page closed itself/),
  ]);
});
