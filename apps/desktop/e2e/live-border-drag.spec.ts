import { test, expect } from './fixtures';
import { EV } from '../src/main/ipc';

// A left border-drag whose grab point lands on a live url WebContentsView must
// still resize the divider. The 10px grab band (resizeBandPx) straddles the
// divider while the view's content box ends 5px (LiveViewInsetPx) inside the
// pane, so the view swallows the press and the preload forwards it as
// EV.leftForward. A handler there that only transfers focus leaves the resize
// unarmed and every later move eaten.
//
// CDP input lands on the canvas and the native view never intercepts it, so a
// real drag cannot reproduce the swallow. This fires EV.leftForward from the
// main process with the coordinates the relay produces, then continues with
// canvas mousemove and mouseup, which is what the wasm sees once the view is
// parked.

test('a forwarded left press in the grab band arms the divider resize', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');

  // On the local origin, so it loads with no network.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickMenuSwatch('url');
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

  // The divider band between the two panes half-overlaps the live view.
  await gw.splitFocusedPaneVertical();
  const panes = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes[0].id, 'live url pane is the left pane').toBe(urlPaneId);
  const before = panes[0].w;
  const gx = panes[0].x + panes[0].w;
  const gy = panes[0].y + panes[0].h / 2;

  // 8px left of the divider: inside the 10px band and past the 5px inset, so on
  // real hardware this press belongs to the live view.
  await electronApp.evaluate(
    ({ BrowserWindow }, { ch, pt }) => {
      BrowserWindow.getAllWindows()[0].webContents.send(ch, pt);
    },
    { ch: EV.leftForward, pt: { x: gx - 8, y: gy } },
  );

  // Arming the resize is also what parks the view.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.leftResizeArmed()), {
      timeout: 5_000,
    })
    .toBe(true);

  // Once parked, the real events land on the canvas.
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
