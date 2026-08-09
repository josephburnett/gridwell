// L3 integration harness for Phase 2. Boots a real Electron main process,
// creates a window + WebviewRegistry, hosts a real WebContentsView on a
// data: URL, then asserts that (a) capturePage yields non-empty JPEG bytes
// and (b) a nav event fires. Run with:
//
//   npm run build && xvfb-run -a electron dist/harness/capture-harness.js
//
// Prints "HARNESS PASS" / "HARNESS FAIL: ..." and exits with 0/1.
import { app, BaseWindow, BrowserWindow } from 'electron';
import { WebviewRegistry } from '../main/webviews';
import { registerWebviewIpc } from '../main/register';
import type { NavEvent } from '../main/ipc';

// The touch-scroll scenario synthesizes TouchEvents inside the hosted page.
// Chromium only exposes the Touch/TouchEvent constructors when touch events
// are on — a real touchscreen turns them on by detection; the harness forces
// them so the synthetic gesture can be built. The production app sets no such
// switch (detection is the product behavior).
app.commandLine.appendSwitch('touch-events', 'enabled');

const DATA_URL =
  'data:text/html,' +
  encodeURIComponent('<title>HarnessTitle</title><body style="margin:0;background:#c0392b">x</body>');

function fail(msg: string): never {
  console.error('HARNESS FAIL:', msg);
  app.exit(1);
  throw new Error(msg); // unreachable; satisfies never
}

async function waitForNonEmptyCapture(
  registry: WebviewRegistry,
  paneId: string,
  timeoutMs: number,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const jpeg = await registry.capture(paneId);
    if (jpeg.length > 0) return jpeg;
    await new Promise((r) => setTimeout(r, 100));
  }
  return '';
}

app.whenReady().then(async () => {
  const navEvents: NavEvent[] = [];
  const win = new BaseWindow({ width: 800, height: 600, show: true });
  const registry = new WebviewRegistry(win, { onNav: (ev) => navEvents.push(ev) });

  registry.place('pane1', 42, 'obj-harness', DATA_URL, { x: 0, y: 0, width: 800, height: 600 });

  const jpeg = await waitForNonEmptyCapture(registry, 'pane1', 6000);
  if (jpeg.length === 0) fail('capturePage produced no frame within 6s');

  // base64 of a JPEG starts with "/9j/".
  if (!jpeg.startsWith('/9j/')) fail(`capture is not JPEG base64 (got prefix ${jpeg.slice(0, 8)})`);
  console.log(`capture ok: ${jpeg.length} base64 chars`);

  // Give nav events a beat to arrive (title update).
  await new Promise((r) => setTimeout(r, 500));
  if (navEvents.length === 0) fail('no nav event fired');
  const last = navEvents[navEvents.length - 1];
  if (last.tileId !== 42) fail(`nav tileId wrong: ${last.tileId}`);
  console.log(`nav ok: title=${JSON.stringify(last.title)} url-prefix=${last.url.slice(0, 16)}`);

  // remove() returns a freeze snapshot.
  const freeze = await registry.remove('pane1');
  if (freeze.jpegBase64.length === 0) fail('freeze snapshot empty');
  if (registry.has('pane1')) fail('pane still registered after remove');
  console.log(`freeze ok: ${freeze.jpegBase64.length} base64 chars, title=${JSON.stringify(freeze.title)}`);

  // ── a dead view yields an empty freeze, never a throw ──────────────────
  // The crashed-tab ascent: every view-bound read in remove() throws once
  // the webContents is gone; remove() must still complete and hand back an
  // empty freeze (the wasm side skips SetURLState on an all-empty result).
  // (The session-dehydrate half of this scenario died 2026-07-26: the
  // Chromium session is host-local now — Chromium's own disk persistence is
  // the system of record, so a crashed view no longer risks losing logins.)
  const reg2 = new WebviewRegistry(win, {});
  await reg2.place('pane2', 43, 'obj-dead', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  if ((await waitForNonEmptyCapture(reg2, 'pane2', 6000)).length === 0) {
    fail('dead-view scenario: view produced no frame within 6s');
  }
  // Destroy the renderer out from under the registry — the crashed-tab shape.
  (reg2 as unknown as { entries: Map<string, { view: { webContents: { close(): void } } }> })
    .entries.get('pane2')!
    .view.webContents.close();
  await new Promise((r) => setTimeout(r, 300));
  const deadFreeze = await reg2.remove('pane2');
  if (deadFreeze.jpegBase64 !== '') fail('dead view yielded a non-empty freeze');
  console.log('dead-view remove ok: empty freeze, no throw');

  // ── single-finger touch scroll over a live view ─────────────────────────
  // A finger drag over live web content must scroll the page, content
  // following the finger (the preload forwards per-move deltas; the registry
  // injects mouseWheel back into the view — see urlview-preload.ts). This
  // crosses the REAL seam: page TouchEvents → preload listener → IPC
  // (production registerWebviewIpc wiring) → registry.touchScroll →
  // sendInputEvent → Chromium scrolls. It also pins the delta SIGN against
  // real Chromium: start mid-page, drag the finger UP, and the scroll offset
  // must INCREASE — a flipped convention would decrease it.
  const rootWin = new BrowserWindow({ show: false });
  const reg3 = new WebviewRegistry(win, {});
  registerWebviewIpc(reg3, rootWin.webContents, win);
  const TALL_URL =
    'data:text/html,' + encodeURIComponent('<body style="margin:0;height:20000px">tall</body>');
  await reg3.place('pane3', 44, 'obj-touch', TALL_URL, { x: 0, y: 0, width: 800, height: 600 });
  if ((await waitForNonEmptyCapture(reg3, 'pane3', 6000)).length === 0) {
    fail('touch scenario: view produced no frame within 6s');
  }
  const wc3 = (
    reg3 as unknown as {
      entries: Map<string, { view: { webContents: Electron.WebContents } }>;
    }
  ).entries.get('pane3')!.view.webContents;
  await wc3.executeJavaScript('window.scrollTo(0, 1000)');
  // The preload forwards physical screen coords; the registry converts them
  // back through getContentBounds + view bounds. The view sits at content
  // (0,0), so screen = content origin + client.
  const cb3 = win.getContentBounds();
  await wc3.executeJavaScript(
    `(() => {
      const mk = (type, x, y) => {
        const t = new Touch({ identifier: 7, target: document.body,
          clientX: x, clientY: y, screenX: x + ${cb3.x}, screenY: y + ${cb3.y} });
        return new TouchEvent(type, { cancelable: true, bubbles: true,
          touches: type === 'touchend' ? [] : [t], changedTouches: [t] });
      };
      window.dispatchEvent(mk('touchstart', 400, 400));
      for (let i = 1; i <= 5; i++) window.dispatchEvent(mk('touchmove', 400, 400 - i * 40));
      window.dispatchEvent(mk('touchend', 400, 200));
    })()`,
  );
  let scrollY = 1000;
  const scrollDeadline = Date.now() + 6000;
  while (Date.now() < scrollDeadline) {
    scrollY = (await wc3.executeJavaScript('window.scrollY')) as number;
    if (scrollY > 1050) break;
    await new Promise((r) => setTimeout(r, 100));
  }
  if (scrollY < 950) fail(`touch scroll DIRECTION flipped: finger up must scroll down (scrollY ${scrollY})`);
  if (scrollY <= 1050) fail(`touch scroll did nothing (scrollY ${scrollY})`);
  console.log(`touch scroll ok: finger up 200px → scrollY 1000 → ${scrollY}`);
  await reg3.remove('pane3');
  rootWin.destroy();

  console.log('HARNESS PASS');
  app.exit(0);
});
