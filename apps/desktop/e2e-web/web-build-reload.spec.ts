import { test as base, expect, Page, Route } from '@playwright/test';
import { seedHome } from '../e2e/fixtures';
import { removeHome } from '../e2e/homes';
import { RestartableServe, Served, freePort, authenticate, authHeaders } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { tileAt } from '../e2e/oracle';
import { dumpViaDoor } from '../e2e/trace';

// A client the node served runs the node's build. The node is restarted
// under a live tab and the tab's calls are made to name another build: the
// shape of a node rebuilt under a tab that was not reloaded, since the door
// judges only the build a call names. Every call is refused before its verb
// (Server.staleBuild); the tab's one answer is nodebuild.Decide's, a notice
// that offers the reload and takes it only when its button is pressed.

const OLD_BUILD = 'e2e-old-build';

type Fixtures = { node: RestartableServe; window: Page; gw: GridwellDriver };

const test = base.extend<Fixtures>({
  node: async ({}, use) => {
    const home = seedHome();
    const srv = new RestartableServe(home, await freePort());
    await srv.start();
    await use(srv);
    await srv.kill();
    removeHome(home);
  },
  window: async ({ node, page }, use) => {
    await authenticate(page, node.served);
    await page.goto(node.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(page);
  },
  gw: async ({ window, node }, use) => {
    await use(new GridwellDriver(window, node.origin));
  },
});

// The node's handshake, asked as a tool rather than a page, so it is not judged.
async function nodeBuild(served: Served): Promise<string> {
  const res = await fetch(`${served.origin}/gridwell.v1.Gridwell/Handshake`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(served) },
    body: '{}',
  });
  return ((await res.json()) as { build?: string }).build ?? '';
}

// Every rpc the page makes names OLD_BUILD until stop() is called.
async function nameAnotherBuild(window: Page): Promise<() => void> {
  let on = true;
  await window.route('**/gridwell.v1.Gridwell/**', async (route: Route) => {
    if (!on) return route.continue();
    await route.continue({ headers: { ...route.request().headers(), 'gridwell-build': OLD_BUILD } });
  });
  return () => {
    on = false;
  };
}

const navigationType = (window: Page) =>
  window.evaluate(() => (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming).type);

// The build notice as the strip holds it, with its button's screen rect.
async function buildNotice(window: Page): Promise<{ message: string; severity: string; button: any } | undefined> {
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  return errs.notices.find((n: any) => n.source === 'build');
}

// A page that reloaded would lose this; the page that keeps it never did.
const markDocument = (window: Page) => window.evaluate(() => ((window as any).__buildSpecMark = true));
const sameDocument = (window: Page) => window.evaluate(() => (window as any).__buildSpecMark === true);

// Long enough that the reload the client used to take on its own would have
// begun: it ran on the first refusal.
const NO_RELOAD_MS = 3_000;

test('a tab whose node changed build offers the reload and lands where it was when asked', async ({
  gw,
  window,
  node,
}) => {
  const pageBuild = await window.evaluate(() => (window as any).__gridwellTest.build());
  expect(pageBuild, 'the served client is stamped').not.toBe('');
  expect(await nodeBuild(node.served), 'the node serves its own build').toBe(pageBuild);

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  await gw.descendCell(cx, cy);
  const place = await gw.focused();
  expect(place.gridID).toBe(well.childGridId);
  // The path is the place's stack of frames; the query is framing, which may
  // settle again before the reload.
  const path = () => new URL(window.url()).pathname;
  await expect.poll(path, { message: 'the descent is in the URL', timeout: 10_000 }).not.toBe('/');
  const placePath = path();

  await node.kill();
  // A pan the dead node cannot take parks; the new node refuses it as this
  // build's, so the notice must name it as what reloading costs.
  const inside = await gw.focused();
  await gw.panFocusedGrid(
    Math.round(inside.cx) + 1,
    Math.round(inside.cy),
    Math.round(inside.cx) - 1,
    Math.round(inside.cy),
  );
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.outbox()), { timeout: 15_000 })
    .toContainEqual(expect.stringMatching(/^SetFraming:/));
  await markDocument(window);
  const stop = await nameAnotherBuild(window);
  await node.start();

  await expect
    .poll(async () => (await buildNotice(window))?.message, {
      message: 'the notice names the parked pan',
      timeout: 30_000,
    })
    .toMatch(/^Gridwell was updated — reload to continue\. \d+ view changes? (was|were) not saved\.$/);
  const notice = (await buildNotice(window))!;
  expect(notice.severity, 'the notice is info, not an error').toBe('info');
  expect(notice.button, 'the notice carries its button').toBeTruthy();

  await window.waitForTimeout(NO_RELOAD_MS);
  expect(
    { same: await sameDocument(window), nav: await navigationType(window) },
    'nothing reloaded on its own',
  ).toEqual({ same: true, nav: 'navigate' });
  // A press on the row beside the button leaves the notice up.
  await window.mouse.click(5, notice.button.y + notice.button.h / 2);
  expect(await buildNotice(window), 'the notice stays until Reload is pressed').toBeTruthy();

  // The reloaded document's load comes before its client boots, so its calls
  // name the page's own build again.
  window.once('load', stop);
  const reloaded = window.waitForEvent('load', { timeout: 60_000 });
  const b = (await buildNotice(window))!.button;
  await window.mouse.click(b.x + b.w / 2, b.y + b.h / 2);
  await reloaded;

  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  expect(await navigationType(window), 'Reload reloaded the page').toBe('reload');
  expect(await sameDocument(window), 'a new document').toBe(false);
  expect(path(), 'the URL held the place').toBe(placePath);
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 15_000 }).toBe(place.gridID);
  expect(await buildNotice(window), 'the reloaded page runs the node build').toBeUndefined();

  // Both halves recorded it: the door's refusal and the page's answer. Read
  // once, not polled: the old page's records die with it, so its answer is
  // in the ring now only because the reload waited for it (handOverBound).
  const lines = await dumpViaDoor(node.origin, node.token);
  expect(
    {
      refused: lines.some((l) => l.src === 'webdoor' && l.kind === 'build' && l.kv?.client === OLD_BUILD),
      answered: lines.some((l) => l.kind === 'build' && l.kv?.verdict === 'offer'),
    },
    'the dump holds the refusal and the answer',
  ).toEqual({ refused: true, answered: true });
});

test('a tab holding unsaved text keeps it on screen and says to copy it first', async ({ gw, window, node }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.typeText('saved before the update. ');
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('saved before the update.');

  await node.kill();
  await gw.typeText('typed while the node was rebuilt.');
  await markDocument(window);
  await nameAnotherBuild(window);
  await node.start();

  await expect
    .poll(async () => (await buildNotice(window))?.message, {
      message: 'the notice says to copy the text first',
      timeout: 30_000,
    })
    .toMatch(/^Gridwell was updated — reload to continue\..* Copy your text first\.$/);
  await window.waitForTimeout(NO_RELOAD_MS);
  expect(await sameDocument(window), 'the page did not reload').toBe(true);
  const value = await window.evaluate(() => (document.querySelector('textarea') as HTMLTextAreaElement | null)?.value ?? '');
  expect(value, 'the unsaved text is still on screen').toContain('typed while the node was rebuilt.');
  expect(await gw.getTileContent(created.id), 'the stale page wrote nothing').not.toContain('typed while');
});
