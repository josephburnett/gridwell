import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Ctrl/Cmd with +, - or 0 zooms a descended tile's content. The zoom is
// per-tile framing, persisted server-side as content_zoom and never bumping the
// version, so a zoomed doc comes back at that size on every descent.

test('Ctrl+= zooms a text tile: persisted as framing, no version bump', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  // Zoom in three steps: 1.1^3, about 1.331, persisted on the tile. Settle
  // between presses. On a slow runner a rapid-fire chord can land while the
  // previous step's redraw is still in flight, and the middle press is lost;
  // what this pins is that the zoom persists as framing, not keyboard
  // rapid-fire.
  for (let i = 0; i < 3; i++) {
    await window.keyboard.press('Control+=');
    await gw.waitIdle();
  }
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
  await gw.enterPlugin('home');
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

// The chord must also work when the live view itself owns OS keyboard focus,
// which is the real descended state and where the window-level keydown never
// fires. Main intercepts the chord in before-input-event, as it does F11, and
// relays it to the wasm zoom owner, so the cache update and the write still run.
test('the zoom chord works when the live view owns keyboard focus', async ({
  electronApp,
  window,
  gw,
}) => {
  // The scratch grid id, where the ephemeral url tile lands, is advertised on
  // the plugin's entry.
  const scratch = (await gw.plugins()).find((l) => l.kind === 'home')!.scratchGridID;
  expect(scratch, 'localdb advertises a scratch grid').toBeTruthy();
  await gw.enterPlugin('home');
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

  // Send the chord to the live view's webContents: the input path a user hits
  // after clicking into the page.
  const sendChord = () =>
    electronApp.evaluate(({ webContents }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('zoom=170'));
      if (!wc) throw new Error('live view not found');
      wc.focus();
      wc.sendInputEvent({ type: 'keyDown', keyCode: '=', modifiers: ['control'] });
      wc.sendInputEvent({ type: 'keyUp', keyCode: '=', modifiers: ['control'] });
    });
  // Two ack counters bracket the relay: main's before-input-event interception
  // (registry.zoomChordRelays) and the wasm owner's receipt across the IPC hop
  // (zoomKeyRelays). They make a lost chord attributable instead of leaving a
  // stuck zoom factor with nothing to say where it was lost.
  const mainRelays = () =>
    electronApp.evaluate(() => ((globalThis as any).__gwRegistry?.zoomChordRelays as number) ?? 0);
  const wasmRelays = () =>
    window.evaluate(() => (window as any).__gridwellTest.zoomKeyRelays() as number);

  // sendInputEvent is fire-and-forget, and under xvfb the synthetic event is
  // occasionally dropped before the input pipeline. That is not the property
  // under test: the relay is interception, then the wasm owner, then the write,
  // and a real keyboard's autorepeat recovers a swallowed keypress the same way.
  // So resend until main acks the interception. Everything downstream of the ack
  // is product path and must work on the first delivery, so those assertions
  // stay hard.
  const sendChordAcked = async () => {
    for (let attempt = 0; attempt < 5; attempt++) {
      const before = await mainRelays();
      await sendChord();
      try {
        await expect.poll(mainRelays, { timeout: 2_000 }).toBeGreaterThan(before);
        return;
      } catch {
        // Never intercepted: the synthetic event was lost, so send again.
      }
    }
    throw new Error('zoom chord never reached before-input-event after 5 sends');
  };
  for (let i = 1; i <= 3; i++) {
    await sendChordAcked();
    // The intercepted chord crossed the IPC hop to the one zoom owner...
    await expect
      .poll(wasmRelays, {
        message: `chord ${i}: intercepted by main but never received by the wasm owner`,
        timeout: 5_000,
      })
      .toBeGreaterThanOrEqual(i);
    // ...and the owner applied it to the composed live-view factor.
    const want = base * Math.pow(1.1, i);
    await expect.poll(factorOf, { timeout: 10_000 }).toBeGreaterThan(want * 0.97);
  }

  // That the forward ran through the wasm owner, and not a main-side
  // setZoomFactor, is what zoomKeyRelays above already witnesses: main
  // intercepted, the owner received, the owner applied. The owner's OTHER
  // half is that it wrote nothing — this visit is ephemeral, off-grid in the
  // scratch grid and deleted on ascent, and a durable framing write about it
  // would mark a row the user never asked to keep (possiblyEphemeral). The
  // browser suite pins the same contract on an ephemeral shell.
  const snap: any = await gw.getGrid(scratch);
  const t = (snap.tiles ?? []).find((t: any) => t.kind === 'url');
  expect(Number(t?.contentZoom ?? 0), 'the ephemeral row is unmarked').toBe(0);
  const posts = await window.evaluate(
    () => Number((window as any).__gridwellTest.persistPosts().SetContentZoom ?? 0),
  );
  expect(posts, 'no SetContentZoom about a row ascent deletes').toBe(0);
});
