import { test, expect } from './fixtures';

// A descent that has visibly animated must happen. Two link-opens out of one
// live page, the second fired while the first pane is provably mid-animation,
// give two panes each animating their own descent. Neither may void the other:
// a transition belongs to a pane, and a displaced one lands on its destination
// rather than vanishing — otherwise the first pane is stranded on the
// animation's scratch viewport with the descent it showed you undone.
//
// The transition clock is stretched through the e2e-only setTransitionMs hook
// so the overlap is deterministic, and transitioning() proves the second
// activation landed inside the window rather than after it.

async function hook<T>(window: any, expr: string): Promise<T> {
  return window.evaluate(`(window).__gridwellTest.${expr}`);
}

// openFromPage fires the page's own new-window intent, the path
// webview_bridge's onOpenBelow forwards. It is one of the entry points that
// does not go through the canvas's gesture gate, which is exactly why it can
// arrive mid-animation.
async function openFromPage(electronApp: any, url: string): Promise<void> {
  await electronApp.evaluate(async ({ webContents }: any, u: string) => {
    const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes('src=page'));
    if (!wc) throw new Error('live view not found');
    await wc.executeJavaScript(`window.open(${JSON.stringify(u)})`, true);
  }, url);
}

test('a second link-open mid-animation does not void the first pane descent', async ({
  electronApp,
  window,
  gw,
}) => {
  const local = (await gw.plugins()).find((l) => l.kind === 'home');
  const scratchGridID = local!.scratchGridID;

  await gw.enterPlugin('home');
  const panesBefore = (await gw.panes()).length;

  // A live ephemeral visit: the page that will pop the two links.
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?src=page`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('src=page')),
        ),
      { timeout: 15_000 },
    )
    .toBe(true);

  // Stretch the clock, then pop the first link and wait until its pane is
  // provably animating.
  await hook(window, 'setTransitionMs(3000)');
  await openFromPage(electronApp, `${gw.origin}/wasm_exec.js?opened=first`);
  await expect
    .poll(() => hook<boolean>(window, 'transitioning()'), { timeout: 15_000 })
    .toBe(true);

  // The second link arrives inside that window. Its own descent must not be
  // paid for with the first one's.
  await openFromPage(electronApp, `${gw.origin}/wasm_exec.js?opened=second`);
  expect(
    await hook<boolean>(window, 'transitioning()'),
    'the second link-open landed inside the animation window',
  ).toBe(true);

  await hook(window, 'setTransitionMs(350)');
  await expect.poll(async () => (await gw.panes()).length, { timeout: 15_000 }).toBe(
    panesBefore + 2,
  );
  await gw.waitIdle(30_000);

  // Both visits exist server-side …
  const scratch = await gw.getGrid(scratchGridID);
  const idOf = (marker: string) =>
    (scratch.tiles ?? []).find((t) => String(t.urlString ?? '').includes(marker))?.id as
      | string
      | undefined;
  const first = idOf('opened=first');
  const second = idOf('opened=second');
  expect(first, 'the first visit tile was created').toBeTruthy();
  expect(second, 'the second visit tile was created').toBeTruthy();

  // … and both panes are inside them. Every pane here descended into
  // something — the source page and the two visits — so a pane sitting on a
  // grid is a descent voided after the user watched it animate.
  const panes = await gw.panes();
  const descended = panes.map((p) => p.textFocus).filter((t) => t !== '');
  expect(descended, 'both link-opens landed on their own tile').toEqual(
    expect.arrayContaining([first!, second!]),
  );
  expect(
    descended.length,
    'a pane was stranded on a grid: its descent animated and then did not happen',
  ).toBe(panes.length);

  // No pane holds a native live view for content it is not inside: a voided
  // descent that popped a content frame while a view stayed placed leaves the
  // page hanging over a grid.
  const viewPanes = await electronApp.evaluate(() => {
    const reg = (globalThis as { __gwRegistry?: { entries: Map<string, unknown> } }).__gwRegistry;
    return reg ? [...reg.entries.keys()] : [];
  });
  const bare = new Set((await gw.panes()).filter((p) => p.textFocus === '').map((p) => p.id));
  expect(
    viewPanes.filter((id) => bare.has(id)),
    'a live view is placed over a pane that is not in a content tile',
  ).toEqual([]);
});
