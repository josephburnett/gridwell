import { test, expect } from './fixtures';

// Issue #111: a link a live view would open in a NEW WINDOW (target=_blank,
// window.open, ctrl/cmd-click) splits the pane and opens as an EPHEMERAL
// visit in the lower half — next to the page it came from, on the same plugin
// session, deleted on ascent like every ephemeral visit.

test('window.open from a live view splits the pane and opens ephemeral below', async ({
  electronApp,
  window,
  gw,
}) => {
  const local = (await (async () => {
    await window.waitForFunction(() => (window as any).__gridwellTest.launcher().length > 0);
    return gw.launcher();
  })()).find((l) => l.kind === 'localdb');
  const scratchGridID = local!.scratchGridID;

  await gw.enterPlugin('localdb');
  const panesBefore = (await gw.panes()).length;

  // A live ephemeral visit to the local origin.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?src=page`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // The page opens a link the way target=_blank / ctrl-click would.
  await electronApp.evaluate(async ({ webContents }, org: string) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('src=page'));
    if (!wc) throw new Error('live view not found');
    await wc.executeJavaScript(`window.open(${JSON.stringify(`${org}/wasm_exec.js?opened=below`)})`, true);
  }, gw.origin);

  // A new pane appears BELOW, focused, descended into an ephemeral url tile.
  await expect.poll(async () => (await gw.panes()).length, { timeout: 15_000 }).toBe(
    panesBefore + 1,
  );
  const panes = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  const lower = panes[panes.length - 1];
  expect(lower.focused, 'the new lower pane took focus').toBe(true);
  expect(lower.y, 'the new pane sits below').toBeGreaterThan(panes[0].y);
  await expect
    .poll(async () => {
      const sc = await gw.getGrid(scratchGridID);
      return (sc.tiles ?? []).filter((t) => String(t.urlString ?? '').includes('opened=below'))
        .length;
    }, { timeout: 10_000 })
    .toBe(1);

  // The SOURCE pane's visit is untouched (its tile still exists) — the
  // clone's file-level ascent must not have deleted it.
  const sc = await gw.getGrid(scratchGridID);
  expect(
    (sc.tiles ?? []).filter((t) => String(t.urlString ?? '').includes('src=page')),
    'the source visit survived the split',
  ).toHaveLength(1);

  // Ascending the new pane deletes its ephemeral visit. Wait out the descent
  // transition first — a click mid-animation is deliberately swallowed — then
  // raw middle-click at the LOWER pane's center (cell math could land in the
  // upper pane).
  await expect
    .poll(async () => (await gw.panes()).find((p) => p.id === lower.id)?.textFocus ?? '', {
      timeout: 10_000,
    })
    .not.toBe('');
  await gw.waitIdle();
  await window.waitForTimeout(500);
  const m = window.mouse;
  await m.move(lower.x + lower.w / 2, lower.y + lower.h / 2);
  await m.down({ button: 'middle' });
  await m.up({ button: 'middle' });
  await gw.waitIdle();
  await expect
    .poll(async () => {
      const g = await gw.getGrid(scratchGridID);
      return (g.tiles ?? []).filter((t) => String(t.urlString ?? '').includes('opened=below'))
        .length;
    }, { timeout: 10_000 })
    .toBe(0);
});
