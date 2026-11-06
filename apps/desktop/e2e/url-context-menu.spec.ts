import { test, expect } from './fixtures';

// A plain right-click over a live url page is the page's: its own contextmenu
// handler runs and Gridwell pops no menu. The bar circle's menu is the pane's
// (url-circle-menu.spec.ts).
//
// A WebContentsView is a separate webContents off the main page, so these tests
// run in the main process through electronApp.evaluate: they place a real live
// view through the registry index.ts exposes under GRIDWELL_E2E, send genuine
// mouse events into that view's webContents, count Menu.popup calls, and read
// what the page's handler saw.

const PAGE =
  'data:text/html,' +
  encodeURIComponent(
    `<!doctype html><meta charset=utf8><title>gwe2ectx</title>` +
      `<style>html,body{margin:0;height:100%}</style><body>page</body>` +
      `<script>window.__gwMenus = []; document.addEventListener('contextmenu', ` +
      `(e) => __gwMenus.push(e.clientX + ',' + e.clientY))</script>`,
  );

// Each move is [x, y]; the press is at the first point, the release at the last.
async function rightClick(electronApp: any, paneId: string, points: number[][]) {
  return electronApp.evaluate(
    async ({ Menu }: any, a: { page: string; paneId: string; points: number[][] }) => {
      const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
      if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');
      await reg.place(a.paneId, 1, a.page, { x: 0, y: 0, width: 800, height: 600 });
      let wc: any = null;
      const findDeadline = Date.now() + 8000;
      while (!wc && Date.now() < findDeadline) {
        wc = reg.webContentsFor(a.paneId);
        if (!wc || wc.getTitle() !== 'gwe2ectx') wc = null;
        if (!wc) await new Promise<void>((res) => setTimeout(res, 50));
      }
      if (!wc) throw new Error('live view did not load');
      const origPopup = Menu.prototype.popup;
      let popups = 0;
      Menu.prototype.popup = function () {
        popups++;
      };
      try {
        const [x0, y0] = a.points[0];
        const [x1, y1] = a.points[a.points.length - 1];
        wc.sendInputEvent({ type: 'mouseDown', x: x0, y: y0, button: 'right', clickCount: 1 });
        for (const [x, y] of a.points.slice(1)) {
          wc.sendInputEvent({ type: 'mouseMove', x, y, modifiers: ['rightButtonDown'] });
        }
        wc.sendInputEvent({ type: 'mouseUp', x: x1, y: y1, button: 'right', clickCount: 1 });
        let menus: string[] = [];
        const deadline = Date.now() + 8000;
        while (menus.length === 0 && Date.now() < deadline) {
          menus = await wc.executeJavaScript('window.__gwMenus');
          if (menus.length === 0) await new Promise<void>((res) => setTimeout(res, 50));
        }
        // Long enough for a second contextmenu or a popup that should not come.
        await new Promise<void>((res) => setTimeout(res, 300));
        return { menus: await wc.executeJavaScript('window.__gwMenus'), popups };
      } finally {
        Menu.prototype.popup = origPopup;
        await reg.remove(a.paneId);
      }
    },
    { page: PAGE, paneId, points },
  );
}

test('a right-click in a live url view reaches the page and pops no menu', async ({ electronApp, window }) => {
  // `window` ensures the app finished booting, so the registry is exposed.
  await window.title();
  const seen = await rightClick(electronApp, 'e2e-ctx', [[100, 100]]);
  expect(seen.menus, "the page's own handler saw the click once, where it landed").toEqual(['100,100']);
  expect(seen.popups, 'Gridwell popped a menu over the page').toBe(0);
});

// Past the 4px distance threshold, but released at once: the classification
// needs both distance and a hold of RIGHT_DRAG_TIME_MS, so this is a click.
test('a jittery right-click (5px movement, fast release) still reaches the page', async ({
  electronApp,
  window,
}) => {
  await window.title();
  const seen = await rightClick(electronApp, 'e2e-jitter', [[100, 100], [105, 100]]);
  expect(seen.menus, 'a jittery click was swallowed as a drag').toEqual(['105,100']);
  expect(seen.popups, 'Gridwell popped a menu over the page').toBe(0);
});
