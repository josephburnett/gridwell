import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The workspace boundary and live tiles: descending into a pane tile is a
// whole-window takeover, so an OUTER pane's live URL view must be frozen
// and torn down (the same flush the pane-collapse path uses — a native
// WebContentsView cannot float over a workspace that replaced its pane),
// and ascending must restore the outer pane still descended into its url,
// re-engaged live on ascent (#202). This is the seam make check cannot see (native
// views live off the main page), hence a real-stack spec.

async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

test('workspace descent freezes an outer live url; ascent revives the descent', async ({ electronApp, gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // A pane tile to enter later.
  await gw.openPalette();
  await gw.dragCreate('pane', cx + 2, cy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', cx + 2, cy);
  expect(pt).toBeTruthy();

  // Go live in this pane: the ephemeral-visit swatch (click, not drag).
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/workspace-live');
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      message: 'the visit must go live (a WebContentsView placed)',
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // Split so a second pane can host the workspace descent while the url
  // pane keeps its live view.
  await gw.splitFocusedPaneVertical();
  const urlPane = (await gw.panes()).find((p: any) => p.textFocus !== '');
  expect(urlPane, 'one pane is descended into the url').toBeTruthy();

  // Descend into the pane tile from the OTHER pane: whole-window takeover.
  await gw.descendCell(cx + 2, cy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  // The outer live view is gone — frozen + torn down at the boundary.
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      message: 'the outer live view must be torn down by the workspace descent',
      timeout: 10_000,
    })
    .toBe(wcBefore);

  // Ascend: the outer arrangement returns — the url pane is still descended
  // into its url tile (TextFocus preserved) and RE-ENGAGES automatically
  // (issue #202): the restore is a re-entry, so the view comes back live.
  const panes = await gw.panes();
  const barTop = Math.max(...panes.map((p: any) => p.y + p.h));
  await window.mouse.click(30, barTop + 13, { button: 'right' });
  await gw.waitIdle();
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const restored = (await gw.panes()).find((p: any) => p.textFocus !== '');
  expect(restored, 'the url descent must survive the round trip').toBeTruthy();
  expect(restored!.textFocus).toBe(urlPane!.textFocus);
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      message: 'the restored descent revives its live view (issue #202)',
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // Teardown: ascend the (revived) ephemeral url so the session ends clean
  // (ascent deletes the scratch tile, issue #85).
  const rp = restored!;
  await window.mouse.click(rp.x + rp.w / 2, rp.y + rp.h / 2, { button: 'middle' });
  await gw.waitIdle();
});
