import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaDoor, TraceLine } from './trace';

// One live surface per content tile, and taking it over moves it: entering a
// pane tile whose layout shows a url tile the outer level shows hands the
// outer pane's view to the inner pane, and leaving hands it back. The same
// WebContentsView serves both, so the page is never reloaded and nothing it
// held is lost: not a script's state, not a half-typed field, not the scroll.

// The page state a reload would lose, read out of the live view. The scroll is
// in CSS px, which a pane of another width rescales by a pixel or so through
// the min-width zoom, so it is compared to the nearest ten.
async function pageState(electronApp: any, paneId: string): Promise<unknown[]> {
  const [mark, typed, y] = await electronApp.evaluate(
    (_e: unknown, id: string) =>
      (globalThis as any).__gwRegistry
        .webContentsFor(id)
        .executeJavaScript(`[window.__gwMark, document.getElementById('gwin').value, window.scrollY]`),
    paneId,
  );
  return [mark, typed, Math.round(y / 10) * 10];
}

test('a url taken over by a pane tile moves there and back without a reload', async ({
  electronApp,
  gw,
  home,
  window,
}) => {
  const origin = new URL(window.url()).origin;
  const token = await loginToken(origin, homePassword(home));
  const liveIn = (paneId: string) =>
    electronApp.evaluate((_e, id) => (globalThis as any).__gwRegistry.has(id) as boolean, paneId);
  const viewId = (paneId: string) =>
    electronApp.evaluate((_e, id) => (globalThis as any).__gwRegistry.webContentsFor(id)?.id as number, paneId);
  const onScreen = (paneId: string) =>
    electronApp.evaluate(
      (_e, id) => ((globalThis as any).__gwRegistry.viewBoundsFor(id)?.x ?? -99999) > -1000,
      paneId,
    );
  const urlViews = () =>
    window.evaluate(() => (window as any).__gridwellTest.urlViews() as Record<string, string>);
  const views = async (): Promise<TraceLine[]> =>
    (await dumpViaDoor(origin, token)).filter((l) => l.origin === 'electron' && l.src === 'webviews');

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // Split first: splitting a descended pane would ascend it.
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(left);
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?takeover=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  await expect.poll(() => liveIn(left.id), { timeout: 15_000 }).toBe(true);
  await gw.waitIdle();
  const tile = (await urlViews())[left.id];
  expect(tile, 'the renderer holds the live view').toBeTruthy();
  const wcId = await viewId(left.id);

  await electronApp.evaluate(
    (_e, id) =>
      (globalThis as any).__gwRegistry.webContentsFor(id).executeJavaScript(
        `window.__gwMark = 'kept';
         document.body.insertAdjacentHTML('afterbegin', '<input id=gwin>');
         document.getElementById('gwin').value = 'half-typed';
         document.body.style.minHeight = '5000px';
         window.scrollTo(0, 400);`,
      ),
    left.id,
  );
  const state = ['kept', 'half-typed', 400];
  expect(await pageState(electronApp, left.id)).toEqual(state);

  // Everything the placement traced is in before the baseline, so a record
  // after it is the round trip's.
  await expect
    .poll(async () => (await views()).some((l) => l.kind === 'nav-done' && l.kv?.tile === tile), { timeout: 20_000 })
    .toBe(true);
  await window.waitForTimeout(1_500);
  const baseline = Math.max(0, ...(await dumpViaDoor(origin, token)).map((l) => l.seq ?? 0));

  // A pane tile from the right pane captures the layout with the url pane in
  // it, so the captured copy of that pane takes the tile's surface over.
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === right.id)!);
  const rf = await gw.focused();
  const px = Math.round(rf.cx) + 2;
  const py = Math.round(rf.cy);
  await gw.openPalette();
  await gw.dragCreate('pane', px, py);
  expect(tileAt(await gw.getGrid(grid), 'pane', px, py)).toBeTruthy();
  await gw.descendCell(px, py);
  await expect.poll(async () => window.evaluate(() => (window as any).__gridwellTest.workspace().depth)).toBe(1);

  let inner = '';
  await expect
    .poll(
      async () => {
        inner = Object.entries(await urlViews()).find(([p, t]) => t === tile && p !== left.id)?.[0] ?? '';
        return inner;
      },
      { message: 'the inner pane took the surface', timeout: 15_000 },
    )
    .not.toBe('');
  await expect.poll(() => liveIn(inner)).toBe(true);
  expect(await liveIn(left.id), 'the outer pane still holds a view').toBe(false);
  expect(await viewId(inner), 'the inner pane got a new view, not the moved one').toBe(wcId);
  await expect.poll(() => onScreen(inner), { message: 'the moved view shows in the inner pane' }).toBe(true);
  expect(await pageState(electronApp, inner), 'the page lost its state entering').toEqual(state);

  // Leaving hands the view back to the outer pane, the same way.
  await gw.leaveWorkspace();
  await expect.poll(async () => window.evaluate(() => (window as any).__gridwellTest.workspace().depth)).toBe(0);
  await expect.poll(async () => (await urlViews())[left.id], { timeout: 15_000 }).toBe(tile);
  await expect.poll(() => liveIn(left.id)).toBe(true);
  expect(await viewId(left.id), 'the outer pane got a new view, not the moved one').toBe(wcId);
  await expect.poll(() => onScreen(left.id), { message: 'the view is back on screen' }).toBe(true);
  expect(await pageState(electronApp, left.id), 'the page lost its state leaving').toEqual(state);

  // Two moves and nothing else: no view made or destroyed, no navigation.
  let after: TraceLine[] = [];
  await expect
    .poll(
      async () => {
        after = (await views()).filter((l) => (l.seq ?? 0) > baseline);
        return after.filter((l) => l.kind === 'move').length;
      },
      { timeout: 20_000 },
    )
    .toBe(2);
  const churn = after.filter(
    (l) => ['create', 'destroy', 'nav-start', 'gone'].includes(l.kind) && (l.kv?.tile === tile || l.kind === 'create'),
  );
  expect(churn, `the round trip rebuilt or reloaded the view: ${JSON.stringify(churn)}`).toEqual([]);

  await gw.focusPane((await gw.panes()).find((p: any) => p.id === left.id)!);
  await gw.middleClickPane();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
});
