import { test, expect } from './fixtures';

// A live url view belongs to the descent it was opened for. When the pane
// leaves that descent the view must be torn down: a native WebContentsView
// left tracking the pane paints over whatever the pane shows next and swallows
// every click over its rect.
//
// A pane too short for two minimum panes visits IN PLACE rather than splitting
// below, and its frame pops to the grid for the whole descent animation while
// the outgoing view is still live. `make check` cannot see this, because the
// view is a separate webContents off the main page, so the assertion reads the
// real registry.

test('a stacked visit in place tears the outgoing live view down as the pane leaves it', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // Drag-create lands the url tile bare; the first descent prompts for the
  // address.
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?stack=a`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  const liveWith = (marker: string) =>
    electronApp.evaluate(
      ({ webContents }, m: string) => webContents.getAllWebContents().some((w) => w.getURL().includes(m)),
      marker,
    );
  await expect.poll(() => liveWith('stack=a'), { timeout: 15_000 }).toBe(true);

  // Shrink the window below two minimum panes plus the bar's reserved row
  // (pane.MinPanePx is 32, wsbar.RowH is 32), so the link's split is refused
  // and the visit lands in this pane.
  await electronApp.evaluate(({ BaseWindow }) => {
    BaseWindow.getAllWindows()[0].setContentSize(1000, 90);
  });
  await expect.poll(async () => (await gw.panes())[0]?.h ?? 999, { timeout: 10_000 }).toBeLessThan(64);

  // Stretch the descent animation so the gap is wide enough to sample: the
  // pane's place pops to the grid at the start of the descent and only lands on
  // the new tile at the end.
  await window.evaluate(() => (window as any).__gridwellTest.setTransitionMs(15_000));

  const regPaneIds = () =>
    electronApp.evaluate(() => {
      const reg = (globalThis as any).__gwRegistry;
      if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');
      return reg.paneIds() as string[];
    });
  const paneId = (await gw.panes())[0].id;
  expect(await regPaneIds(), 'the pane holds a live view before the visit').toContain(paneId);

  // The page opens a link the way target=_blank or a ctrl-click would. Too
  // short to split, the visit descends in place.
  await electronApp.evaluate(async ({ webContents }, org: string) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('stack=a'));
    if (!wc) throw new Error('live view not found');
    await wc.executeJavaScript(`window.open(${JSON.stringify(`${org}/wasm_exec.js?stack=b`)})`, true);
  }, gw.origin);

  // While the pane is between the two tiles, showing the grid with no content
  // frame, no live view may be attached to it.
  await expect
    .poll(
      async () => {
        const p = (await gw.panes()).find((q) => q.id === paneId);
        if (!p) return 'gone';
        if (p.textFocus !== '') return 'descended'; // not started, or already landed
        return (await regPaneIds()).includes(paneId) ? 'pinned' : 'torn-down';
      },
      {
        message: 'a pane that left its descent must not keep a native page pinned over it',
        timeout: 10_000,
      },
    )
    .toBe('torn-down');

  // The descent still completes, which is what the teardown must not break.
  await expect.poll(() => liveWith('stack=b'), { timeout: 30_000 }).toBe(true);
});
