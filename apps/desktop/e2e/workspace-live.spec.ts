import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Descending into a pane tile keeps the outer level fully alive: its live views
// park off-screen and keep running, so a hidden call keeps ringing, and its
// shells stay attached. Liveness follows pane existence: a pane's resources run
// until the pane closes, and leaving a view closes that view's panes. Nothing
// freezes at the boundary, so nothing needs reviving on return; the parked view
// comes back on screen. `make check` cannot see this seam, since native views
// live off the main page.

async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

test('workspace descent keeps the outer live url running, parked; ascent shows it again', async ({ electronApp, gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // A pane tile to enter later.
  await gw.openPalette();
  await gw.dragCreate('pane', cx + 2, cy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', cx + 2, cy);
  expect(pt).toBeTruthy();

  // Go live in this pane through the ephemeral-visit swatch: click, not drag.
  // Ephemeral visits are stripped from the layout capture, so the fresh workspace
  // will not clone this pane. No one-surface takeover applies, and the outer view
  // must simply keep running, hidden.
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

  // viewBounds finds the live view's rect; parked views sit far off-screen.
  const viewBounds = () =>
    electronApp.evaluate(({ webContents, BaseWindow }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('workspace-live'));
      if (!wc) return null;
      const win = BaseWindow.getAllWindows()[0];
      const v = (win.contentView.children as unknown as { webContents?: { id: number }; getBounds(): { x: number; y: number } }[]).find(
        (c) => c.webContents?.id === wc.id,
      );
      return v ? v.getBounds() : null;
    });
  await expect.poll(async () => (await viewBounds())?.x ?? -99999, { timeout: 10_000 }).toBeGreaterThan(-1000);

  // Split so a second pane can host the workspace descent while the url
  // pane keeps its live view.
  await gw.splitFocusedPaneVertical();
  const urlPane = (await gw.panes()).find((p: any) => p.textFocus !== '');
  expect(urlPane, 'one pane is descended into the url').toBeTruthy();

  // Descend into the pane tile from the other pane: a whole-window takeover.
  await gw.descendCell(cx + 2, cy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);

  // The outer live view is still running: its webContents survives, parked
  // off-screen because its pane is not in the current layout.
  await window.waitForTimeout(1_000); // give a wrong teardown time to fire
  const parked = await viewBounds();
  expect(parked, 'the outer live view survives the descent').not.toBeNull();
  expect(parked!.x, 'and is parked off-screen').toBeLessThan(-1000);

  // Ascend: the outer arrangement returns, the url pane is still descended, and
  // the same webContents comes back on screen. It never reloaded, because it
  // never stopped.
  await gw.leaveWorkspace();
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const restored = (await gw.panes()).find((p: any) => p.textFocus !== '');
  expect(restored, 'the url descent must survive the round trip').toBeTruthy();
  expect(restored!.textFocus).toBe(urlPane!.textFocus);
  await expect.poll(async () => (await viewBounds())?.x ?? -99999, {
    message: 'the parked view returns to the screen',
    timeout: 15_000,
  }).toBeGreaterThan(-1000);

  // Teardown: ascend the ephemeral url so the session ends clean; the ascent
  // deletes the scratch tile.
  const rp = (await gw.panes()).find((p: any) => p.textFocus !== '')!;
  await window.mouse.click(rp.x + rp.w / 2, rp.y + rp.h / 2, { button: 'middle' });
  await gw.waitIdle();
});
