import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #82: Ctrl/Cmd +/-/0 zooms a descended tile's CONTENT. The zoom is
// per-tile FRAMING — persisted server-side (content_zoom), never bumping the
// version — so a zoomed doc comes back at your size on every descent.

test('Ctrl+= zooms a text tile: persisted as framing, no version bump', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // Zoom in three steps: 1.1^3 ≈ 1.331, persisted on the tile.
  for (let i = 0; i < 3; i++) await window.keyboard.press('Control+=');
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)?.contentZoom ?? 0), {
      timeout: 10_000,
    })
    .toBeCloseTo(1.331, 2);

  // Framing, not content: the version did not move.
  const after = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  expect(after.version, 'no version bump from zooming').toBe(created.version);

  // Ctrl+0 resets.
  await window.keyboard.press('Control+0');
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)?.contentZoom ?? 0))
    .toBeCloseTo(1.0, 3);
});

test('Ctrl+= zooms a live url view (composed with the layout zoom)', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?zoom=82`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const factorOf = () =>
    electronApp.evaluate(({ webContents }) => {
      const zs = webContents
        .getAllWebContents()
        .map((wc) => {
          try {
            return wc.getURL().includes('zoom=82') ? wc.getZoomFactor() : 0;
          } catch {
            return 0;
          }
        })
        .filter((z) => z > 0);
      return zs[0] ?? 0;
    });

  await expect.poll(factorOf, { timeout: 10_000 }).toBeGreaterThan(0);
  const base = await factorOf();
  for (let i = 0; i < 3; i++) await window.keyboard.press('Control+=');
  await expect
    .poll(factorOf, { timeout: 10_000 })
    .toBeCloseTo(Math.min(base * 1.331, 3), 1);
});

// Issue #170: the chord must ALSO work when the live view itself owns OS
// keyboard focus — the real descended state, where the window-level keydown
// never fires. Main intercepts the chord in before-input-event (like F11)
// and relays it to the wasm zoom owner, so cache + persistence still run.
test('the zoom chord works when the live view owns keyboard focus', async ({
  electronApp,
  window,
  gw,
}) => {
  // The scratch grid id (where the ephemeral url tile lands) is advertised
  // on the plugin's entry.
  const scratch = (await gw.plugins()).find((l) => l.kind === 'localdb')!.scratchGridID;
  expect(scratch, 'localdb advertises a scratch grid').toBeTruthy();
  await gw.enterPlugin('localdb');
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?zoom=170`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const factorOf = () =>
    electronApp.evaluate(({ webContents }) => {
      const zs = webContents
        .getAllWebContents()
        .map((wc) => {
          try {
            return wc.getURL().includes('zoom=170') ? wc.getZoomFactor() : 0;
          } catch {
            return 0;
          }
        })
        .filter((z) => z > 0);
      return zs[0] ?? 0;
    });
  await expect.poll(factorOf, { timeout: 10_000 }).toBeGreaterThan(0);
  const base = await factorOf();

  // Send the chord TO the live view's webContents — the input path a user
  // hits after clicking into the page.
  const sendChord = () =>
    electronApp.evaluate(({ webContents }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('zoom=170'));
      if (!wc) throw new Error('live view not found');
      wc.focus();
      wc.sendInputEvent({ type: 'keyDown', keyCode: '=', modifiers: ['control'] });
      wc.sendInputEvent({ type: 'keyUp', keyCode: '=', modifiers: ['control'] });
    });
  for (let i = 0; i < 3; i++) await sendChord();

  // The view zoom moves (composed atop the min-width layout zoom)…
  await expect.poll(factorOf, { timeout: 10_000 }).toBeGreaterThan(base * 1.25);

  // …and the zoom is PERSISTED as framing on the (scratch) tile — proof the
  // forward ran through the wasm owner, not a main-side setZoomFactor.
  await expect
    .poll(
      async () => {
        const snap: any = await gw.getGrid(scratch);
        const t = (snap.tiles ?? []).find((t: any) => t.kind === 'url');
        return Number(t?.contentZoom ?? 0);
      },
      { timeout: 10_000 },
    )
    .toBeCloseTo(1.331, 2);
});
