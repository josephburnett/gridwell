import { test, expect } from './fixtures';
import { EV } from '../src/main/ipc';

// Issue #81: a LEFT border-drag whose grab point lands on a live URL
// WebContentsView must still resize the divider. The 10px grab band straddles
// the divider and the live view's content box ends only 5px (LiveViewInsetPx)
// inside the pane, so the inner half of the band IS the live view: the view
// swallows the real press and the preload forwards it (VIEW_LEFTDOWN →
// EV.leftForward → wasm onForwardedLeftDown). Historically that handler only
// transferred focus — the resize never armed, the view never parked, and every
// subsequent move was eaten ("moves a little then stops"), while the
// right-button twin ran the full classification and worked.
//
// CDP-injected Playwright input lands on the MAIN webContents (the canvas) and
// is never intercepted by the native view, so a real mouse drag cannot
// reproduce "the view ate the press". Instead this spec drives the exact seam
// the fix lives in: it fires EV.leftForward from the main process with the
// grab-band coordinates (what the preload+main relay produces for a real
// press), then continues the drag with synthetic canvas mousemove/mouseup —
// which is precisely what the wasm sees once arming parks the view.

test('a forwarded left press in the grab band arms the divider resize', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');

  // A live URL view via the ephemeral-visit swatch, on the local origin so it
  // loads instantly with no network.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/?b81=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  const urlPaneId = (await gw.focused()).id;

  // Split: the live url pane keeps the left half, the new pane takes focus on
  // the right. The divider band between them half-overlaps the live view.
  await gw.splitFocusedPaneVertical();
  const panes = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes[0].id, 'live url pane is the left pane').toBe(urlPaneId);
  const before = panes[0].w;
  const gx = panes[0].x + panes[0].w;
  const gy = panes[0].y + panes[0].h / 2;

  // The forwarded press: 8px left of the divider — inside the 10px grab band,
  // past the 5px inset, i.e. a point that on real hardware belongs to the
  // live view. This is the exact payload main relays for such a press.
  await electronApp.evaluate(
    ({ BrowserWindow }, { ch, pt }) => {
      BrowserWindow.getAllWindows()[0].webContents.send(ch, pt);
    },
    { ch: EV.leftForward, pt: { x: gx - 8, y: gy } },
  );

  // Continue the drag on the canvas (post-park, that is where the real events
  // land): a held-button move to the target, then release.
  await window.waitForTimeout(500);
  await window.evaluate(
    ([tx, ty]: number[]) => {
      const c = document.querySelector('canvas')!;
      c.dispatchEvent(
        new MouseEvent('mousemove', { clientX: tx, clientY: ty, buttons: 1, bubbles: true }),
      );
      c.dispatchEvent(
        new MouseEvent('mouseup', { clientX: tx, clientY: ty, button: 0, bubbles: true }),
      );
    },
    [gx - 200, gy],
  );
  await gw.waitIdle();

  const after = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  expect(before - after.w, 'the url pane shrank by roughly the drag distance').toBeGreaterThan(100);
});
