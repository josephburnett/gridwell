import { Route } from '@playwright/test';
import { test, expect } from './fixtures';
import { createExitWell } from './oracle';

// A renderer whose node runs another build offers the reload in a notice
// (nodebuild.Decide) and reloads only when its button is pressed; the reload
// must take its live url views down on the way out: a native WebContentsView
// is not part of the page, so one the reload leaves behind paints over the
// reloaded canvas and swallows its clicks. The sidecar cannot be rebuilt under
// a running app, so the page's calls are made to name another build, the shape
// the door judges.

const OLD_BUILD = 'e2e-old-build';

test('a renderer offered a reload for its build reloads when asked, leaving no live view behind', async ({
  electronApp,
  gw,
  window,
}) => {
  const regPaneIds = () =>
    electronApp.evaluate(() => (globalThis as any).__gwRegistry.paneIds() as string[]);
  const liveWith = (marker: string) =>
    electronApp.evaluate(
      ({ webContents }, m: string) =>
        webContents.getAllWebContents().filter((w) => !w.isDestroyed() && w.getURL().includes(m)).length,
      marker,
    );

  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?reload=views`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await expect.poll(() => liveWith('reload=views'), { timeout: 15_000 }).toBe(1);

  // Back out and open it live in a pane below, so the grid above it is shown
  // and an event on it makes the client call the node.
  await gw.middleClickPane();
  await expect.poll(() => regPaneIds()).toEqual([]);
  await gw.ctrlDescendCell(cx, cy);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const below = await gw.focused();
  await expect.poll(() => regPaneIds(), { timeout: 15_000 }).toEqual([below.id]);
  await gw.waitIdle();

  let stale = true;
  await window.route('**/gridwell.v1.Gridwell/**', async (route: Route) => {
    if (!stale) return route.continue();
    await route.continue({ headers: { ...route.request().headers(), 'gridwell-build': OLD_BUILD } });
  });
  const buildNotice = async () =>
    (await window.evaluate(() => (window as any).__gridwellTest.errors().notices)).find(
      (n: any) => n.source === 'build',
    );
  await window.evaluate(() => ((window as any).__buildSpecMark = true));
  await createExitWell(gw.origin, home.gridID, 'nowhere/1', 'poke', cx + 2, cy);
  await expect
    .poll(async () => (await buildNotice())?.message, { message: 'the notice offers the reload', timeout: 30_000 })
    .toMatch(/^Gridwell was updated — reload to continue\./);
  // Long enough that the reload the client used to take on its own would have
  // begun: it ran on the first refusal.
  await window.waitForTimeout(3_000);
  expect(
    {
      same: await window.evaluate(() => (window as any).__buildSpecMark === true),
      views: await regPaneIds(),
    },
    'nothing reloaded on its own, and the live view is still up',
  ).toEqual({ same: true, views: [below.id] });

  // The reloaded document's load comes before its client boots, so its calls
  // name the page's own build again.
  const reloaded = window.waitForEvent('load', { timeout: 60_000 });
  window.once('load', () => {
    stale = false;
  });
  const b = (await buildNotice()).button;
  await window.mouse.click(b.x + b.w / 2, b.y + b.h / 2);
  await reloaded;

  expect(
    await regPaneIds(),
    'the old page took its live view down before the new one booted',
  ).toEqual([]);
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  expect(
    await window.evaluate(() => (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming).type),
  ).toBe('reload');

  // The URL held the focused pane's place: the url tile, live again in one view.
  const after = await gw.focused();
  await expect.poll(() => regPaneIds(), { timeout: 20_000 }).toEqual([after.id]);
  await expect.poll(() => liveWith('reload=views'), { timeout: 15_000 }).toBe(1);
  const notices = await window.evaluate(() => (window as any).__gridwellTest.errors().notices);
  expect(
    notices.filter((n: any) => /replaced .* without a close/.test(n.message)),
    'no view was left for the new page to replace',
  ).toEqual([]);
});
