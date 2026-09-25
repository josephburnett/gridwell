import { test, expect } from './fixtures';

// A pan is a left-drag on empty cells: it moves its own pane's viewport, draws
// no ghost and can drop nowhere. It must leave every other pane's live url view
// alone. One window-wide park verdict made it park them all, so every pan — and
// every bare click, which arms the same drag — blanked the page in the pane
// next door and brought it back on release.
//
// pane.ParkSurface owns the verdict per pane now. The registry's hidden flag is
// read from the main process, the only place a WebContentsView's state lives.

test('a pan in one pane never parks the live url view in another', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const panes = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes.length, 'the split made two side-by-side panes').toBe(2);
  const [left, right] = panes;

  // The left pane goes live on a url served by the node itself, so it loads
  // with no network.
  await gw.focusPane(left);
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/?pan-live=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  expect((await gw.focused()).id, 'the left pane holds the live visit').toBe(left.id);

  const hiddenOf = (paneId: string) =>
    electronApp.evaluate((_electron, id) => {
      const reg = (globalThis as { __gwRegistry?: { entries: Map<string, { hidden: boolean }> } })
        .__gwRegistry;
      if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');
      const e = reg.entries.get(id);
      return e ? e.hidden : null;
    }, paneId);

  expect(await hiddenOf(left.id), 'the live view is on screen before the gesture').toBe(false);

  // Pan the right pane, which is still on its grid. Its center lands on no
  // tile, so the press arms a pan and not a tile drag.
  const r = (await gw.panes()).find((p) => p.id === right.id)!;
  await gw.focusPane(r);
  const cxBefore = (await gw.panes()).find((p) => p.id === right.id)!.cx;
  const sx = r.x + r.w / 2;
  const sy = r.y + r.h / 2;
  await window.mouse.move(sx, sy);
  await window.mouse.down();

  // Sampled through the main process, so each read gives the renderer's IPC for
  // the frame before it time to land.
  const samples: (boolean | null)[] = [];
  for (let i = 1; i <= 6; i++) {
    await window.mouse.move(sx - i * 15, sy - i * 8, { steps: 3 });
    samples.push(await hiddenOf(left.id));
  }
  const draggingMidPan = await window.evaluate(
    () => (window as { __gridwellTest?: any }).__gridwellTest.idleDetail().dragging,
  );
  await window.mouse.up();
  await gw.waitIdle();

  expect(draggingMidPan, 'the pan was armed while the samples were taken').toBe(true);
  expect(samples, 'the live view stayed on screen for every frame of the pan').toEqual([
    false,
    false,
    false,
    false,
    false,
    false,
  ]);

  const cxAfter = (await gw.panes()).find((p) => p.id === right.id)!.cx;
  expect(cxAfter, 'the pan moved the right pane, so the gesture was real').not.toBe(cxBefore);
  expect(await hiddenOf(left.id), 'and it is still on screen after the release').toBe(false);
});
