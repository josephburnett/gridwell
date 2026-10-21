import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// An ephemeral url visit is a url tile with no home: it writes the address
// it lands on, like any url tile, and the only thing ephemeral means is that
// the row is deleted when the visit ends and nothing holds it. Inside a
// workspace tile the layout holds it, so it reopens where it was left; outside
// one the ascent deletes it, after the address write it caused.

async function workspaceDepth(window: any): Promise<number> {
  return (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
}

const sameID = (id: string, qualified: string) => id === qualified || qualified.endsWith('/' + id);

async function openVisit(window: any, gw: any, address: string): Promise<string> {
  await gw.clickMenuSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', address);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  return (await gw.focused()).textFocus;
}

function liveURL(electronApp: any, mark: string) {
  return () =>
    electronApp.evaluate(({ webContents }: any, m: string) => {
      const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(m));
      return wc ? { url: wc.getURL(), loading: wc.isLoading() } : { url: '', loading: true };
    }, mark);
}

async function navigate(electronApp: any, mark: string, target: string): Promise<void> {
  await electronApp.evaluate(async ({ webContents }: any, a: { m: string; target: string }) => {
    const wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(a.m));
    if (!wc) throw new Error('live view not found');
    await wc.executeJavaScript(`location.href = ${JSON.stringify(a.target)}`);
  }, { m: mark, target });
}

test('an ephemeral url visit inside a workspace tile reopens where it was left', async ({ electronApp, window, gw }) => {
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const scratchGridID = await gw.scratchGridID('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const pt = tileAt(await gw.getGrid(f.gridID), 'workspace', wx, wy)!;
  expect(pt, 'workspace tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);

  const visit = await openVisit(window, gw, `${gw.origin}/wasm_exec.js?eph=1`);
  const live = liveURL(electronApp, 'eph=');
  await expect.poll(live, { timeout: 15_000 }).toMatchObject({ loading: false });
  await navigate(electronApp, 'eph=', `${gw.origin}/wasm_exec.js?eph=2`);
  await expect.poll(async () => (await live()).url, { timeout: 15_000 }).toContain('eph=2');

  const row = async () =>
    ((await gw.getGrid(scratchGridID)).tiles ?? []).find((t: any) => sameID(String(t.id), visit));
  await expect
    .poll(async () => String((await row())?.urlString ?? ''), {
      message: 'the visit writes the address it landed on, like any url tile',
      timeout: c.urlAddressMs * 20,
    })
    .toContain('eph=2');
  await expect
    .poll(async () => {
      try {
        return (await gw.getTileContent(pt.id)).includes(visit) ? 'recorded' : '';
      } catch {
        return '';
      }
    }, { message: 'the layout names the visit', timeout: 10_000 })
    .toBe('recorded');

  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  await gw.waitIdle();
  expect(String((await row())?.urlString ?? ''), 'the row the layout holds keeps the second address').toContain('eph=2');

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect
    .poll(async () => (await live()).url, { message: 'the visit reopens on the second page', timeout: 15_000 })
    .toContain('eph=2');

  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no notices from the round trip').toHaveLength(0);
});

test('an ephemeral url visit that wrote its address is still deleted on ascent', async ({ electronApp, window, gw }) => {
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const scratchGridID = await gw.scratchGridID('home');
  const home = await gw.focused();

  const visit = await openVisit(window, gw, `${gw.origin}/wasm_exec.js?gone=1`);
  const live = liveURL(electronApp, 'gone=');
  await expect.poll(live, { timeout: 15_000 }).toMatchObject({ loading: false });
  const row = async () =>
    ((await gw.getGrid(scratchGridID)).tiles ?? []).find((t: any) => sameID(String(t.id), visit));
  const opened = await row();
  expect(opened, 'the visit is a scratch-grid row').toBeTruthy();

  // A settled navigation bumps the version while the view is live.
  await navigate(electronApp, 'gone=', `${gw.origin}/wasm_exec.js?gone=2`);
  await expect
    .poll(async () => String((await row())?.urlString ?? ''), { timeout: c.urlAddressMs * 20 })
    .toContain('gone=2');
  // proto3 JSON omits a zero version.
  expect(Number((await row())!.version ?? 0)).toBeGreaterThan(Number(opened!.version ?? 0));

  // A navigation the ascent overtakes is written by the close, so the
  // delete must claim the version that write produced.
  await navigate(electronApp, 'gone=', `${gw.origin}/wasm_exec.js?gone=3`);
  await expect.poll(async () => (await live()).url, { timeout: 15_000 }).toContain('gone=3');
  await gw.middleClickCell(0, 0);
  expect((await gw.focused()).gridID, 'back on the home grid').toBe(home.gridID);

  await expect
    .poll(async () => (await row()) === undefined, {
      message: 'the ascent deletes the visit, whatever it wrote',
      timeout: 10_000,
    })
    .toBe(true);
  await gw.waitIdle();
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no refused delete or failed write surfaced').toHaveLength(0);
});
