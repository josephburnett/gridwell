// Integration harness for the registry: a real Electron main process, a real
// WebContentsView, and the scenarios below run against them. It prints
// "HARNESS PASS" or "HARNESS FAIL: ..." and exits 0 or 1.
//
//   npm run build && xvfb-run -a electron dist/harness/capture-harness.js
import { app, BaseWindow, BrowserWindow, Menu, WebContentsView } from 'electron';
import type { WebContents } from 'electron';
import * as fs from 'node:fs';
import * as http from 'node:http';
import * as os from 'node:os';
import * as path from 'node:path';
import { WebviewRegistry } from '../main/webviews';
import { registerWebviewIpc } from '../main/register';
import type { ErrorEvent, NavEvent } from '../main/ipc';
import { PARK_COORD, SESSION_PARTITION } from '../main/viewutil';
import { FOCUS_SETTLE_MS } from '../main/focusguard';

// A throwaway Chromium profile per run, set before app is ready: the
// storage-flush scenario asserts what is on disk under SESSION_PARTITION, and a
// previous run's bytes would answer for this one. The sweep runs at the start
// because app.exit() runs no process 'exit' handler, and Chromium writes a
// fresh profile skeleton on its way out anyway.
const PROFILE_PREFIX = 'gridwell-harness-';
for (const name of fs.readdirSync(os.tmpdir())) {
  if (!name.startsWith(PROFILE_PREFIX)) continue;
  try {
    fs.rmSync(path.join(os.tmpdir(), name), { recursive: true, force: true });
  } catch {
    // Another harness may be running; its profile is not ours to reclaim.
  }
}
const profileDir = fs.mkdtempSync(path.join(os.tmpdir(), PROFILE_PREFIX));
app.setPath('userData', profileDir);

// Chromium exposes the Touch and TouchEvent constructors only when touch events
// are on, which a real touchscreen does by detection. The production app sets no
// such switch, because detection is the product behavior.
app.commandLine.appendSwitch('touch-events', 'enabled');

const DATA_URL =
  'data:text/html,' +
  encodeURIComponent('<title>HarnessTitle</title><body style="margin:0;background:#c0392b">x</body>');

function fail(msg: string): never {
  console.error('HARNESS FAIL:', msg);
  app.exit(1);
  throw new Error(msg); // unreachable; satisfies never
}

// A load is an event, so waiting for it costs a cold machine nothing. A frame
// is a budget, because Chromium announces no first paint. Keeping them apart is
// what stops a runner that has just unpacked Electron from spending the frame
// budget on its first renderer.
const LOAD_BUDGET_MS = 30_000;
const FRAME_BUDGET_MS = 6_000;

// Resolves on the view's next did-finish-load. Every caller arms this in the
// same turn that started the navigation, and Chromium emits from the event
// loop, so the event cannot have fired already.
function loadFinished(wc: WebContents, what: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`no did-finish-load for ${what} within ${LOAD_BUDGET_MS}ms`)),
      LOAD_BUDGET_MS,
    );
    wc.once('did-finish-load', () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

// The first frame a freshly placed view paints. The load is awaited as an
// event first, so FRAME_BUDGET_MS bounds rendering alone, and a failure says
// which half ran out.
async function waitForFirstFrame(
  registry: WebviewRegistry,
  paneId: string,
  what: string,
): Promise<string> {
  const wc = registry.webContentsFor(paneId);
  if (!wc) fail(`${what}: no view registered for ${paneId}`);
  const loadStart = Date.now();
  await loadFinished(wc, what);
  const loadMs = Date.now() - loadStart;
  const frameStart = Date.now();
  while (Date.now() - frameStart < FRAME_BUDGET_MS) {
    const jpeg = await registry.capture(paneId);
    if (jpeg.length > 0) {
      console.log(`${what}: loaded in ${loadMs}ms, first frame ${Date.now() - frameStart}ms after`);
      return jpeg;
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  fail(`${what}: loaded in ${loadMs}ms, then produced no frame within ${FRAME_BUDGET_MS}ms`);
}

// How long /slow holds its body open. Long enough that a mirror tick lands
// inside the navigation on a loaded box, short enough not to pad the run.
const SLOW_BODY_MS = 1500;

// A data: url has an opaque origin, so it has no localStorage and no
// same-document navigation; the scenarios that need either need a real one.
// /slow commits and then keeps loading, which is the gap a mirror tick must
// not read.
async function startPage(): Promise<{ url: string; slowUrl: string; close: () => void }> {
  const server = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/html' });
    const body = '<title>HarnessPage</title><body>page</body>';
    if (req.url === '/slow') {
      setTimeout(() => res.end(body), SLOW_BODY_MS);
      return;
    }
    res.end(body);
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
  const port = (server.address() as { port: number }).port;
  const origin = `http://127.0.0.1:${port}`;
  return { url: `${origin}/`, slowUrl: `${origin}/slow`, close: () => server.close() };
}

app.whenReady().then(async () => {
  const navEvents: NavEvent[] = [];
  const win = new BaseWindow({ width: 800, height: 600, show: true });
  const registry = new WebviewRegistry(win, { onNav: (ev) => navEvents.push(ev) });

  await registry.place('pane1', 'u1/42', DATA_URL, { x: 0, y: 0, width: 800, height: 600 });

  const jpeg = await waitForFirstFrame(registry, 'pane1', 'first capture');

  // base64 of a JPEG starts with "/9j/".
  if (!jpeg.startsWith('/9j/')) fail(`capture is not JPEG base64 (got prefix ${jpeg.slice(0, 8)})`);
  console.log(`capture ok: ${jpeg.length} base64 chars`);

  // Give nav events a beat to arrive (title update).
  await new Promise((r) => setTimeout(r, 500));
  if (navEvents.length === 0) fail('no nav event fired');
  const last = navEvents[navEvents.length - 1];
  if (last.tileId !== 'u1/42') fail(`nav tileId wrong: ${last.tileId}`);
  console.log(`nav ok: title=${JSON.stringify(last.title)} url-prefix=${last.url.slice(0, 16)}`);

  // remove() returns a freeze snapshot.
  const freeze = await registry.remove('pane1');
  if (freeze.jpegBase64.length === 0) fail('freeze snapshot empty');
  if (registry.has('pane1')) fail('pane still registered after remove');
  console.log(`freeze ok: ${freeze.jpegBase64.length} base64 chars, title=${JSON.stringify(freeze.title)}`);

  // ── goBack walks the view's real navigation history ─────────────────────
  // The bar's back button and the context menu's Back share one owner, pinned
  // here against Electron's navigationHistory on a real view.
  const FIRST_URL = 'data:text/html,' + encodeURIComponent('<title>First</title>first');
  const SECOND_URL = 'data:text/html,' + encodeURIComponent('<title>Second</title>second');
  await registry.place('paneb', 'u1/46', FIRST_URL, { x: 0, y: 0, width: 400, height: 300 });
  const wcb = registry.webContentsFor('paneb')!;
  const loaded = (url: string) => loadFinished(wcb, url.slice(0, 40));
  if (wcb.getURL() !== FIRST_URL) await loaded(FIRST_URL);
  const second = loaded(SECOND_URL);
  await wcb.loadURL(SECOND_URL);
  await second;
  if (wcb.getURL() !== SECOND_URL) fail(`second load did not land (url ${wcb.getURL().slice(0, 40)})`);
  const back = loaded(FIRST_URL);
  registry.goBack('paneb');
  await back;
  if (wcb.getURL() !== FIRST_URL) fail(`goBack did not return to the first url (url ${wcb.getURL().slice(0, 40)})`);
  registry.goBack('paneb'); // at the start of the history: a no-op, never a throw
  await registry.remove('paneb');
  console.log('goBack ok: second → first, no-op at the start');

  // ── a view placed while an overlay is open starts parked ────────────────
  // A view placed while the palette is open must land at PARK_COORD, never on
  // top of the canvas overlay for even one round trip.
  const hiddenBounds = { x: 10, y: 20, width: 300, height: 200 };
  await registry.place('pane1h', 'u1/45', DATA_URL, hiddenBounds, 0, '', false, true);
  const parked = registry.viewBoundsFor('pane1h');
  if (parked?.x !== PARK_COORD) fail(`place(hidden=true) did not park the view (x=${parked?.x})`);
  registry.setHidden('pane1h', false, true);
  const shown = registry.viewBoundsFor('pane1h');
  if (shown?.x !== hiddenBounds.x || shown?.y !== hiddenBounds.y) {
    fail(`un-park after a hidden place landed at (${shown?.x},${shown?.y}), want (${hiddenBounds.x},${hiddenBounds.y})`);
  }
  await registry.remove('pane1h');
  console.log('hidden place ok: parked at PARK_COORD, un-parked to its bounds');

  // ── a dead view yields an empty freeze, never a throw ──────────────────
  // Ascending out of a crashed tab: every view-bound read in remove() throws,
  // and remove() must still complete with an empty freeze, which the wasm side
  // skips writing back. The crash is reported, so the user knows why.
  const deadErrs: string[] = [];
  const reg2 = new WebviewRegistry(win, { onError: (ev) => deadErrs.push(ev.message) });
  await reg2.place('pane2', 'u1/43', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  await waitForFirstFrame(reg2, 'pane2', 'dead-view scenario');
  // Destroy the renderer out from under the registry, the crashed-tab shape.
  reg2.webContentsFor('pane2')!.close();
  await new Promise((r) => setTimeout(r, 300));
  const deadFreeze = await reg2.remove('pane2');
  if (deadFreeze.jpegBase64 !== '') fail('dead view yielded a non-empty freeze');
  if (!deadErrs.some((m) => m.includes('view crashed while closing'))) {
    fail(`a crash during remove was not reported (errors: ${JSON.stringify(deadErrs)})`);
  }
  console.log('dead-view remove ok: empty freeze, no throw, the crash reported');

  // ── single-finger touch scroll over a live view ─────────────────────────
  // The whole seam: page TouchEvents, preload listener, IPC through the
  // production wiring, registry.touchScroll, sendInputEvent, Chromium scrolls.
  // It also pins the delta sign: drag the finger up, the offset must increase.
  const rootWin = new BrowserWindow({ show: false });
  const reg3 = new WebviewRegistry(win, {});
  registerWebviewIpc(reg3, rootWin.webContents, win);
  const TALL_URL =
    'data:text/html,' + encodeURIComponent('<body style="margin:0;height:20000px">tall</body>');
  await reg3.place('pane3', 'u1/44', TALL_URL, { x: 0, y: 0, width: 800, height: 600 });
  await waitForFirstFrame(reg3, 'pane3', 'touch scenario');
  const wc3 = reg3.webContentsFor('pane3')!;
  await wc3.executeJavaScript('window.scrollTo(0, 1000)');
  // The view sits at content (0,0), so screen equals the content origin plus
  // the client position.
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

  // ── a view placed on an unfocused pane may not keep OS focus ────────────
  // A pane goes live on paths that are not a gesture on the focused pane, such
  // as a workspace restore, and attaching a view hands the new widget focus. A
  // registry that assumed the placement was focused would land the user's next
  // keystrokes in a page they never clicked on.
  const steals: string[] = [];
  const regF = new WebviewRegistry(win, { onFocusStolen: (ev) => steals.push(ev.paneId) });
  await regF.place('paneU', 'u1/47', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', false, false, false);
  if (regF.focusedFor('paneU') !== false) {
    fail(`place(focused=false) recorded focused=${String(regF.focusedFor('paneU'))}`);
  }
  const wcU = regF.webContentsFor('paneU')!;
  // Chromium emits 'focus' on the webContents, which is where the guard sits.
  wcU.focus();
  const stealDeadline = Date.now() + 5000;
  while (steals.length === 0 && Date.now() < stealDeadline) {
    await new Promise((r) => setTimeout(r, 100));
  }
  if (steals[0] !== 'paneU') fail('a view placed on an unfocused pane kept OS focus: no steal reported');
  // The focused case is not bounced, or every legitimate descent would fight
  // the guard.
  const regG = new WebviewRegistry(win, { onFocusStolen: (ev) => steals.push('WRONG:' + ev.paneId) });
  await regG.place('paneG', 'u1/48', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', false, false, true);
  if (regG.focusedFor('paneG') !== true) fail('place(focused=true) did not record focused');
  const wcG = regG.webContentsFor('paneG')!;
  wcG.focus();
  await new Promise((r) => setTimeout(r, 500));
  if (steals.some((s) => s.startsWith('WRONG:'))) fail('a view placed on the FOCUSED pane was bounced');
  await regF.remove('paneU');
  await regG.remove('paneG');
  console.log('place focus ok: unfocused placement bounced, focused placement kept');

  // ── the user's own click into an unfocused live pane is not a steal ──────
  // At the `focus` event neither the browser-process input-event nor the
  // preload's IPC has arrived, so a guard deciding there would report a steal
  // for the click the user just made. The order below is the measured one:
  // focus first, then a real sendInputEvent press. Only the widget-focus half
  // is stood in for, because sendInputEvent cannot produce a `focus` event.
  const rootView = new WebContentsView({ webPreferences: { sandbox: false } });
  win.contentView.addChildView(rootView);
  rootView.setBounds({ x: 0, y: 0, width: 800, height: 600 });
  await rootView.webContents.loadURL('data:text/html,' + encodeURIComponent('<title>root</title>root'));

  const clickSteals: string[] = [];
  // Wired as index.ts wires it: a reported steal hands OS focus back to the
  // root webContents.
  const regC = new WebviewRegistry(win, {
    onFocusStolen: (ev) => {
      clickSteals.push(ev.paneId);
      rootView.webContents.focus();
    },
  });
  await regC.place('paneC', 'u1/49', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', false, false, false);
  const wcC = regC.webContentsFor('paneC')!;
  // Let the placement's own grab be bounced and focus land back on the root, so
  // the click below starts from an unfocused live pane whose view does not hold
  // OS focus.
  await new Promise((r) => setTimeout(r, 1000));
  rootView.webContents.focus();
  await new Promise((r) => setTimeout(r, 300));
  if (wcC.isFocused()) fail('setup: the view still held OS focus before the click');
  clickSteals.length = 0;

  wcC.focus(); // the widget focus Chromium applies while routing the press
  wcC.sendInputEvent({ type: 'mouseDown', x: 200, y: 150, button: 'left', clickCount: 1 });
  wcC.sendInputEvent({ type: 'mouseUp', x: 200, y: 150, button: 'left', clickCount: 1 });
  await new Promise((r) => setTimeout(r, 800));
  if (clickSteals.length > 0) {
    fail(`the user's own click into an unfocused live pane was reported as a steal (${clickSteals.length}x)`);
  }
  if (!wcC.isFocused()) fail("the user's own click did not leave the view holding OS focus");
  await regC.remove('paneC');
  console.log('click-into-unfocused ok: the press explains the focus, no bounce');

  // The precondition every steal starts from: an unfocused pane with OS focus
  // on the root. The placement's own grab is bounced first, so the recorder
  // starts empty.
  const armSteal = async (paneId: string, tileId: string, steals: string[]) => {
    const reg = new WebviewRegistry(win, { onFocusStolen: (ev) => steals.push(ev.paneId) });
    await reg.place(paneId, tileId, DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', false, false, false);
    const wc = reg.webContentsFor(paneId)!;
    await new Promise((r) => setTimeout(r, 800));
    rootView.webContents.focus();
    await new Promise((r) => setTimeout(r, 300));
    if (wc.isFocused()) fail(`${paneId}: setup left the view holding OS focus`);
    steals.length = 0;
    return { reg, wc };
  };
  const waitFor = async (ok: () => boolean, timeoutMs: number): Promise<boolean> => {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      if (ok()) return true;
      await new Promise((r) => setTimeout(r, 5));
    }
    return ok();
  };

  // ── a page grab is bounced, then confirmed once, then stops ──────────────
  // The widget-focus commit can land after the bounce with no further focus
  // event, so the guard confirms once at FOCUS_SETTLE_MS. Nothing here hands
  // focus back, so the second bounce fires; it must then stop, or a chain would
  // report a steal every 120 ms forever.
  const settleSteals: string[] = [];
  const { reg: regD, wc: wcD } = await armSteal('paneD', 'u1/50', settleSteals);
  wcD.focus(); // the page-initiated grab: no press behind it
  if (!(await waitFor(() => settleSteals.length >= 2, 4000))) {
    fail(`the settle arm did not confirm the bounce (${settleSteals.length} steals)`);
  }
  await new Promise((r) => setTimeout(r, 600));
  if (settleSteals.length !== 2) fail(`the bounce chained past its confirmation (${settleSteals.length} steals)`);
  await regD.remove('paneD');
  console.log('settle arm ok: bounce, one confirmation, no chain');

  // ── a press between the bounce and its confirmation stops the second ─────
  // The user clicks into the pane just after a page grabbed focus. The press is
  // counted in the browser process, the confirmation sees the count risen, and
  // the view keeps the focus the click gave it.
  const raceSteals: string[] = [];
  const { reg: regE, wc: wcE } = await armSteal('paneE', 'u1/51', raceSteals);
  wcE.focus();
  if (!(await waitFor(() => raceSteals.length >= 1, 4000))) fail('paneE: the first bounce never fired');
  wcE.sendInputEvent({ type: 'mouseDown', x: 200, y: 150, button: 'left', clickCount: 1 });
  wcE.sendInputEvent({ type: 'mouseUp', x: 200, y: 150, button: 'left', clickCount: 1 });
  await new Promise((r) => setTimeout(r, 600));
  if (raceSteals.length !== 1) fail(`a press during the settle did not stop the second bounce (${raceSteals.length} steals)`);
  await regE.remove('paneE');
  console.log('press-during-settle ok: the second bounce is suppressed');

  // ── the view dies under the settle timer ────────────────────────────────
  // remove() cancels the timer only for a teardown that went through the
  // registry. A crash or a host-side close leaves it armed over a destroyed
  // WebContents, where every read throws uncaught inside a timer and hangs main
  // behind an error dialog.
  const deadSteals: string[] = [];
  const { reg: regH, wc: wcH } = await armSteal('paneH', 'u1/52', deadSteals);
  const uncaught: string[] = [];
  const onUncaught = (err: Error) => uncaught.push(String(err));
  process.on('uncaughtException', onUncaught);
  wcH.focus();
  if (!(await waitFor(() => wcH.isFocused(), 3000))) fail('paneH: the grab never landed');
  wcH.close(); // destroyed behind the registry's back, timer still armed
  await new Promise((r) => setTimeout(r, 800));
  process.off('uncaughtException', onUncaught);
  if (uncaught.length > 0) fail(`the settle timer threw over a destroyed view: ${uncaught[0]}`);
  if (deadSteals.length > 0) fail(`a destroyed view reported a steal (${deadSteals.length})`);
  await regH.remove('paneH');
  console.log('died-under-settle ok: no throw, no phantom steal');

  // ── a place() into a pane that already holds a view is loud ─────────────
  // The renderer closes a pane's live view before placing another, so this
  // branch is a renderer bug. The replaced view's freeze has no caller, so the
  // preview is lost and this report is the only evidence.
  const replaceErrs: ErrorEvent[] = [];
  const regR = new WebviewRegistry(win, { onError: (ev) => replaceErrs.push(ev) });
  await regR.place('paneR', 'u1/60', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  const replacedWc = regR.webContentsFor('paneR')!;
  await regR.place('paneR', 'u1/61', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  if (replaceErrs.length !== 1) fail(`a replaced live view reported ${replaceErrs.length} errors, want 1`);
  if (replaceErrs[0].source !== 'electron:webview') fail(`the replace report came from ${replaceErrs[0].source}`);
  if (!replaceErrs[0].message.includes('u1/60 → u1/61')) {
    fail(`the replace report does not name both tiles: ${replaceErrs[0].message}`);
  }
  // The replacement stands and the old view is gone: one entry, the new tile,
  // and the replaced renderer destroyed rather than left painting over it.
  if (regR.paneIds().length !== 1) fail(`the replace left ${regR.paneIds().length} entries for one pane`);
  if (regR.tileIdFor('paneR') !== 'u1/61') fail(`the replacement did not take (tile ${regR.tileIdFor('paneR')})`);
  if (regR.webContentsFor('paneR') === replacedWc) fail('the replaced view is still the registered one');
  if (!(await waitFor(() => replacedWc.isDestroyed(), 4000))) fail('the replaced view leaked: its webContents is alive');
  await regR.remove('paneR');
  console.log('place replace ok: reported, old view destroyed, replacement stands');

  // ── a refused back-stack is the only thing that says so ─────────────────
  // navigationHistory.restore rejects for an entry Chromium will not load, and
  // a url it refuses outright commits nothing and fires no did-fail-load, so
  // the pane sits blank: this report is the whole notice.
  const REFUSED_URL = 'view-source:http://127.0.0.1:1/';
  const refusedHistory = JSON.stringify({
    index: 1,
    entries: [
      { url: DATA_URL, title: 'HarnessTitle' },
      { url: REFUSED_URL, title: 'refused' },
    ],
  });
  const refuseErrs: ErrorEvent[] = [];
  const regRefuse = new WebviewRegistry(win, { onError: (ev) => refuseErrs.push(ev) });
  await regRefuse.place('paneS', 'u1/63', REFUSED_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, refusedHistory);
  if (!(await waitFor(() => refuseErrs.length > 0, 8000))) {
    fail('a refused back-stack reported nothing: the pane is blank and the user is not told');
  }
  const refusal = refuseErrs[0];
  if (refusal.source !== 'electron:webview') fail(`the refusal came from ${refusal.source}`);
  if (!refusal.message.includes('paneS') || !refusal.message.includes('back-stack refused')) {
    fail(`the refusal does not name the pane and what happened: ${refusal.message}`);
  }
  if (!refusal.message.includes('ERR_')) fail(`the refusal carries no reason: ${refusal.message}`);
  // Nothing else speaks for this failure, which is why the report exists.
  await new Promise((r) => setTimeout(r, 1000));
  if (refuseErrs.length !== 1) {
    fail(`a refusal reported ${refuseErrs.length} times: ${JSON.stringify(refuseErrs.map((ev) => ev.message))}`);
  }
  // Nothing loaded, and the stack was left where Chromium refused it.
  const atRefuse = regRefuse.webContentsFor('paneS')!.getURL();
  if (!atRefuse.includes('127.0.0.1:1')) fail(`the refused entry is not what the pane sits on: ${atRefuse}`);
  await regRefuse.remove('paneS');
  console.log('restore refusal ok: reported once, with its reason, by nothing else');

  // ── setBounds returns early when nothing moved ──────────────────────────
  // syncURLViews calls setBounds every frame, so the equal-bounds early return
  // is the difference between a no-op and re-applying the composed zoom sixty
  // times a second. Zoom makes it observable, because applyMinWidthZoom is the
  // only thing in the registry that touches zoomFactor.
  const regB = new WebviewRegistry(win, {});
  const narrow = { x: 0, y: 0, width: 320, height: 300 };
  await regB.place('paneB2', 'u1/62', DATA_URL, narrow);
  const wcB = regB.webContentsFor('paneB2')!;
  if (!(await waitFor(() => wcB.getZoomFactor() < 1, 6000))) {
    fail(`the min-width zoom never applied (factor ${wcB.getZoomFactor()})`);
  }
  wcB.setZoomFactor(1);
  regB.setBounds('paneB2', { ...narrow });
  if (wcB.getZoomFactor() !== 1) fail('setBounds with equal bounds did work: the zoom was re-applied');
  regB.setBounds('paneB2', { ...narrow, width: narrow.width + 1 });
  if (wcB.getZoomFactor() >= 1) fail(`a real bounds change did not re-apply the zoom (factor ${wcB.getZoomFactor()})`);
  await regB.remove('paneB2');
  console.log('setBounds ok: equal bounds are a no-op, a changed width recomputes the zoom');

  // ── setHidden carries a focus change with no view change ────────────────
  // Focus and visibility ride the same call because syncURLViews reports both
  // every frame. A focus-only change must be recorded and must not move the
  // view, or a focus move would blank live content.
  const regFo = new WebviewRegistry(win, {});
  const foBounds = { x: 5, y: 6, width: 400, height: 300 };
  await regFo.place('paneFo', 'u1/63', DATA_URL, foBounds, 0, '', false, false, false);
  const beforeFo = regFo.viewBoundsFor('paneFo');
  regFo.setHidden('paneFo', false, true);
  if (regFo.focusedFor('paneFo') !== true) fail('a focus-only setHidden was not recorded on the entry');
  const afterFo = regFo.viewBoundsFor('paneFo');
  if (afterFo?.x !== beforeFo?.x || afterFo?.y !== beforeFo?.y) {
    fail(`a focus-only setHidden moved the view (${beforeFo?.x},${beforeFo?.y}) → (${afterFo?.x},${afterFo?.y})`);
  }
  regFo.setHidden('paneFo', false, false);
  if (regFo.focusedFor('paneFo') !== false) fail('a focus-only setHidden back to unfocused was not recorded');
  await regFo.remove('paneFo');
  console.log('setHidden focus ok: tracked on the entry, the view never moved');

  // ── remove() cannot detach: the blank-overlay report ────────────────────
  // Destroying the window under the registry, which is what a host-side close
  // does, makes removeChildView throw. remove() must still complete and say why
  // a blank rectangle is covering a pane.
  const detachErrs: string[] = [];
  const doomed = new BaseWindow({ width: 300, height: 200, show: false });
  const regX = new WebviewRegistry(doomed, { onError: (ev) => detachErrs.push(ev.message) });
  await regX.place('paneX', 'u1/64', DATA_URL, { x: 0, y: 0, width: 300, height: 200 });
  doomed.destroy();
  await new Promise((r) => setTimeout(r, 200));
  await regX.remove('paneX'); // must resolve, never throw
  if (!detachErrs.some((m) => m.includes('failed to detach live view'))) {
    fail(`a failed detach was not reported (errors: ${JSON.stringify(detachErrs)})`);
  }
  console.log('detach failure ok: remove resolved and the blank overlay was reported');

  // ── remove() commits DOM storage before the renderer dies ───────────────
  // Chromium flushes localStorage lazily, so an abrupt close can drop a site's
  // unsubmitted draft. The claim is about bytes on disk, so this reads the
  // partition's own leveldb before and after.
  const lsPage = await startPage();
  const MARKER = 'GWFLUSHMARKER12345';
  const lsDir = path.join(
    profileDir,
    'Partitions',
    SESSION_PARTITION.replace(/^persist:/, ''),
    'Local Storage',
    'leveldb',
  );
  const markerOnDisk = (): boolean => {
    try {
      return fs
        .readdirSync(lsDir)
        .some((f) => fs.readFileSync(path.join(lsDir, f)).includes(MARKER));
    } catch {
      return false; // no leveldb yet is no marker
    }
  };
  const regS = new WebviewRegistry(win, {});
  await regS.place('paneS', 'u1/65', lsPage.url, { x: 0, y: 0, width: 400, height: 300 });
  const wcS = regS.webContentsFor('paneS')!;
  if (!(await waitFor(() => wcS.getURL() === lsPage.url, 6000))) fail('storage page never loaded');
  await wcS.executeJavaScript(`localStorage.setItem('gwdraft', ${JSON.stringify(MARKER)})`);
  // Chromium batches DOM-storage commits by seconds; nothing between the write
  // and the remove is slow enough for one, so the write is still in memory.
  if (markerOnDisk()) fail('precondition broke: Chromium committed the write before remove() flushed');
  await regS.remove('paneS');
  if (!markerOnDisk()) fail('remove() did not commit localStorage: the draft died with the view');
  lsPage.close();
  console.log('storage flush ok: the write was in memory, remove() put it on disk');

  // ── remove() cancels a pending settle timer ─────────────────────────────
  // The closure holds the view, and firing after close() would throw uncaught
  // in main. The cancel is invisible from outside, so this records every
  // FOCUS_SETTLE_MS timeout the registry arms and checks the pending one.
  const armedSettles: unknown[] = [];
  const clearedSettles: unknown[] = [];
  const realSetTimeout = globalThis.setTimeout;
  const realClearTimeout = globalThis.clearTimeout;
  (globalThis as { setTimeout: unknown }).setTimeout = (fn: () => void, ms?: number, ...rest: unknown[]) => {
    const t = (realSetTimeout as (...a: unknown[]) => unknown)(fn, ms, ...rest);
    if (ms === FOCUS_SETTLE_MS) armedSettles.push(t);
    return t;
  };
  (globalThis as { clearTimeout: unknown }).clearTimeout = (t: unknown) => {
    if (armedSettles.includes(t)) clearedSettles.push(t);
    return (realClearTimeout as (...a: unknown[]) => unknown)(t);
  };
  const cancelSteals: string[] = [];
  const { reg: regT, wc: wcT } = await armSteal('paneT', 'u1/66', cancelSteals);
  wcT.focus(); // a page grab: the guard waits, bounces, then arms its confirmation
  if (!(await waitFor(() => cancelSteals.length >= 1, 4000))) fail('paneT: the first bounce never fired');
  if (armedSettles.length === 0) fail('paneT: no settle timer was armed');
  const pending = armedSettles[armedSettles.length - 1];
  await regT.remove('paneT');
  (globalThis as { setTimeout: unknown }).setTimeout = realSetTimeout;
  (globalThis as { clearTimeout: unknown }).clearTimeout = realClearTimeout;
  if (!clearedSettles.includes(pending)) fail('remove() left the pending settle timer armed');
  await new Promise((r) => setTimeout(r, FOCUS_SETTLE_MS * 4));
  if (cancelSteals.length !== 1) fail(`a removed pane kept bouncing (${cancelSteals.length} steals)`);
  console.log('settle cancel ok: the pending timer was cleared and never fired again');

  // ── F11 inside a live view toggles the host window ──────────────────────
  // window.ts owns F11 on the canvas, but a focused live view holds OS keyboard
  // focus, so the registry mirrors it. The host window is a stand-in, because
  // real fullscreen needs a window manager and there is none under xvfb. What
  // is pinned is the relay: the key reaches the window's own toggle.
  let fullScreen = false;
  const fsCalls: boolean[] = [];
  const fakeWin = {
    contentView: win.contentView,
    getContentBounds: () => win.getContentBounds(),
    isFullScreen: () => fullScreen,
    setFullScreen: (v: boolean) => {
      fullScreen = v;
      fsCalls.push(v);
    },
  } as unknown as BaseWindow;
  const regK = new WebviewRegistry(fakeWin, {});
  await regK.place('paneK', 'u1/67', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  const wcK = regK.webContentsFor('paneK')!;
  wcK.focus();
  wcK.sendInputEvent({ type: 'keyDown', keyCode: 'F11' });
  wcK.sendInputEvent({ type: 'keyUp', keyCode: 'F11' });
  if (!(await waitFor(() => fsCalls.length >= 1, 4000))) fail('F11 in a live view never reached the window');
  if (fsCalls[0] !== true) fail('the first F11 did not enter fullscreen');
  wcK.sendInputEvent({ type: 'keyDown', keyCode: 'F11' });
  wcK.sendInputEvent({ type: 'keyUp', keyCode: 'F11' });
  if (!(await waitFor(() => fsCalls.length >= 2, 4000))) fail('the second F11 never reached the window');
  if (fsCalls[1] !== false) fail('F11 does not toggle: the second press did not leave fullscreen');
  await regK.remove('paneK');
  console.log('F11 relay ok: the key toggles the host window, both directions');

  // ── a same-document navigation is still a navigation ────────────────────
  // A hash change or a pushState fires did-navigate-in-page only, and both
  // change the tile's address, which is what the bar shows and what a freeze
  // persists, so both must emit.
  const inPageNavs: NavEvent[] = [];
  const navPage = await startPage();
  const regN = new WebviewRegistry(win, { onNav: (ev) => inPageNavs.push(ev) });
  await regN.place('paneN', 'u1/69', navPage.url, { x: 0, y: 0, width: 400, height: 300 });
  const wcN = regN.webContentsFor('paneN')!;
  if (!(await waitFor(() => wcN.getURL() === navPage.url, 6000))) fail('the nav page never loaded');
  inPageNavs.length = 0;
  await wcN.executeJavaScript('location.hash = "#deep"');
  if (!(await waitFor(() => inPageNavs.some((n) => n.url.endsWith('#deep')), 4000))) {
    fail('a hash navigation emitted no nav event');
  }
  await wcN.executeJavaScript('history.pushState({}, "", "/pushed")');
  if (!(await waitFor(() => inPageNavs.some((n) => n.url.endsWith('/pushed')), 4000))) {
    fail('a pushState emitted no nav event');
  }
  if (inPageNavs.some((n) => n.tileId !== 'u1/69')) fail('an in-page nav event carried the wrong tile');
  await regN.remove('paneN');
  navPage.close();
  console.log('in-page nav ok: a hash change and a pushState both report the new address');

  // ── a crashed renderer says so ──────────────────────────────────────────
  // Unreported, the view just sits blank. The message names the page, so
  // getURL() is read on a webContents whose renderer has just died, inside a
  // try/catch so a throw cannot swallow the notice.
  const crashErrs: string[] = [];
  const regP = new WebviewRegistry(win, { onError: (ev) => crashErrs.push(ev.message) });
  await regP.place('paneP', 'u1/70', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  await waitForFirstFrame(regP, 'paneP', 'crash scenario');
  regP.webContentsFor('paneP')!.forcefullyCrashRenderer();
  if (!(await waitFor(() => crashErrs.some((m) => m.startsWith('page crashed')), 6000))) {
    fail(`a crashed renderer was not reported (errors: ${JSON.stringify(crashErrs)})`);
  }
  const crashMsg = crashErrs.find((m) => m.startsWith('page crashed'))!;
  if (!crashMsg.includes('data:text/html')) fail(`the crash report does not name the page: ${crashMsg}`);
  await regP.remove('paneP');
  console.log('render-process-gone ok: the crash is reported and names the page');

  // ── the min-width zoom survives a navigation ────────────────────────────
  // Chromium resets zoomFactor across a navigation, so a narrow pane would
  // reflow to a cramped mobile layout unless did-finish-load re-applies it.
  const regZ = new WebviewRegistry(win, {});
  await regZ.place('paneZ', 'u1/71', DATA_URL, { x: 0, y: 0, width: 320, height: 300 });
  const wcZ = regZ.webContentsFor('paneZ')!;
  if (!(await waitFor(() => wcZ.getZoomFactor() < 1, 6000))) fail('the min-width zoom never applied');
  wcZ.setZoomFactor(1); // whatever the new document would start at
  await wcZ.loadURL('data:text/html,' + encodeURIComponent('<title>Second</title>second'));
  if (!(await waitFor(() => wcZ.getZoomFactor() < 1, 6000))) {
    fail(`a navigation dropped the min-width zoom (factor ${wcZ.getZoomFactor()})`);
  }
  await regZ.remove('paneZ');
  console.log('zoom re-apply ok: the composed factor is back after a navigation');

  // ── a mirror capture that fails leaves evidence, once per streak ────────
  // Otherwise the pane shows a stale frame and nothing says why. The pump
  // captures on a timer, so a per-frame report would bury the log.
  const capErrs: ErrorEvent[] = [];
  const regM = new WebviewRegistry(win, { onError: (ev) => capErrs.push(ev) });
  await regM.place('paneM', 'u1/72', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  await waitForFirstFrame(regM, 'paneM', 'mirror scenario');
  regM.webContentsFor('paneM')!.close(); // destroyed behind the registry's back
  await new Promise((r) => setTimeout(r, 300));
  if ((await regM.capture('paneM')) !== '') fail('a capture of a destroyed view returned a frame');
  if ((await regM.capture('paneM')) !== '') fail('the second capture of a destroyed view returned a frame');
  const failing = capErrs.filter((e) => e.message.includes('mirror capture failing'));
  if (failing.length !== 1) fail(`a failing capture streak reported ${failing.length} times, want 1`);
  // The severity rides the same event the message does, all the way from the
  // report site: client/errsurface paints the row from it.
  if (failing[0].severity !== 'error') fail(`a frozen mirror was reported as '${failing[0].severity}'`);
  await regM.remove('paneM');
  console.log('capture streak ok: the failure is reported once, not per frame, as an error');

  // ── a tick inside a main-frame navigation is not an attempt ─────────────
  // Between the old document's surface going away and the new one's first
  // frame there is nothing to read, and capturePage rejects there
  // (UnknownVizError on the Linux dev box). That is a page loading, not a
  // frozen mirror, so the tick is skipped: the mirror keeps its last frame and
  // nothing is reported.
  const navPage2 = await startPage();
  const gapErrs: string[] = [];
  const regGap = new WebviewRegistry(win, { onError: (ev) => gapErrs.push(ev.message) });
  await regGap.place('paneGap', 'u1/77', navPage2.url, { x: 0, y: 0, width: 400, height: 300 });
  await waitForFirstFrame(regGap, 'paneGap', 'nav-gap scenario');
  const wcGap = regGap.webContentsFor('paneGap')!;
  const started = new Promise<void>((r) => {
    wcGap.once('did-start-navigation', () => r());
  });
  const finished = loadFinished(wcGap, 'the slow page');
  void wcGap.loadURL(navPage2.slowUrl);
  await started;
  // The old document still has a surface here, so a capture would succeed and
  // land a frame the pane is no longer showing; later in the same gap it
  // rejects. Neither is an attempt.
  const during = await regGap.capture('paneGap');
  if (during !== '') fail(`a mirror tick inside a navigation captured ${during.length} base64 chars`);
  if (gapErrs.length !== 0) fail(`a skipped tick reported: ${JSON.stringify(gapErrs)}`);
  await finished;
  let after = '';
  const afterDeadline = Date.now() + FRAME_BUDGET_MS;
  while (Date.now() < afterDeadline && !after) {
    after = await regGap.capture('paneGap');
    if (!after) await new Promise((r) => setTimeout(r, 100));
  }
  if (!after) fail(`the mirror never captured again after the navigation (errors: ${JSON.stringify(gapErrs)})`);
  if (gapErrs.length !== 0) fail(`the navigation left a notice behind: ${JSON.stringify(gapErrs)}`);
  await regGap.remove('paneGap');
  navPage2.close();
  console.log('nav-gap ok: a tick inside a navigation is skipped, and the mirror comes back after it');

  // ── a crashed renderer's frozen mirror is reported, and its recovery too ─
  // A crashed renderer is not a destroyed view: capturePage still answers, with
  // an empty image, a rejection, or nothing before the time box. Each opens the
  // streak, and reloading closes it.
  const streakErrs: ErrorEvent[] = [];
  const regCrash = new WebviewRegistry(win, { onError: (ev) => streakErrs.push(ev) });
  await regCrash.place('paneCrash', 'u1/75', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  await waitForFirstFrame(regCrash, 'paneCrash', 'streak scenario');
  // A crash sometimes takes the viz process with it under xvfb, and then
  // capturePage rejects for every view forever. A control pane that never
  // crashed tells that environment collapse from the product bug.
  const ctlErrs: string[] = [];
  const regCtl = new WebviewRegistry(win, { onError: (ev) => ctlErrs.push(ev.message) });
  await regCtl.place('paneCtl', 'u1/76', DATA_URL, { x: 400, y: 0, width: 400, height: 300 });
  await waitForFirstFrame(regCtl, 'paneCtl', 'control pane');
  const wcCrash = regCrash.webContentsFor('paneCrash')!;
  wcCrash.forcefullyCrashRenderer();
  // Capture until the report lands, so a crash that takes a moment to reach
  // capturePage is not read as a missing report.
  let sawFailing = false;
  const failDeadline = Date.now() + 8000;
  while (Date.now() < failDeadline) {
    if ((await regCrash.capture('paneCrash')) === '' && streakErrs.some((e) => e.message.includes('mirror capture failing'))) {
      sawFailing = true;
      break;
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  if (!sawFailing) {
    fail(`a crashed renderer's frozen mirror was never reported (errors: ${JSON.stringify(streakErrs)})`);
  }
  const failMsg = streakErrs.find((e) => e.message.includes('mirror capture failing'))!.message;
  if (!failMsg.includes('paneCrash')) fail(`the failing report does not name the pane: ${failMsg}`);

  wcCrash.reload();
  let recoveredFrame = '';
  let ctlMisses = 0;
  const recDeadline = Date.now() + 10000;
  while (Date.now() < recDeadline) {
    recoveredFrame = await regCrash.capture('paneCrash');
    if (recoveredFrame) break;
    ctlMisses = (await regCtl.capture('paneCtl')) ? 0 : ctlMisses + 1;
    if (ctlMisses >= 3) break; // the compositor, not this pane
    await new Promise((r) => setTimeout(r, 100));
  }
  if (!recoveredFrame && ctlMisses >= 3) {
    // Nothing in this process can capture, so there is no recovery to observe;
    // capturestreak.test.ts owns the transition.
    console.log(
      'capture streak SKIPPED the recovery half: the crash took the compositor with it, ' +
        `so the untouched control pane stopped capturing too (xvfb artifact) — control said ${JSON.stringify(ctlErrs)}`,
    );
  } else {
    if (!recoveredFrame) fail(`a reloaded renderer never captured again (errors: ${JSON.stringify(streakErrs)})`);
    const recovered = streakErrs.filter((e) => e.message.includes('mirror capture recovered'));
    if (recovered.length !== 1) {
      fail(`recovery was reported ${recovered.length} times, want 1 (errors: ${JSON.stringify(streakErrs)})`);
    }
    // A mirror that is live again is information; an error row would say
    // something failed just as it stopped failing.
    if (recovered[0].severity !== 'info') fail(`a recovery was reported as '${recovered[0].severity}'`);
    // The streak is closed, so a further good capture says nothing more.
    if ((await regCrash.capture('paneCrash')).length === 0) fail('a recovered mirror stopped capturing');
    if (streakErrs.filter((e) => e.message.includes('mirror capture recovered')).length !== 1) {
      fail('a healthy capture re-reported recovery');
    }
    if (streakErrs.filter((e) => e.message.includes('mirror capture failing')).length !== 1) {
      fail(`the failing report fired more than once per streak: ${JSON.stringify(streakErrs)}`);
    }
    console.log('capture streak ok: a crashed renderer reports failing as an error, and a reload reports recovered as info');
  }
  await regCtl.remove('paneCtl');
  await regCrash.remove('paneCrash');

  // ── Freeze Page appears only where there is something to freeze ─────────
  // canFreeze is the registry's half of the gating; contextmenu.test.ts owns
  // the template's arms. An ephemeral visit has no tile to re-descend into, so
  // offering the item would promise a freeze that lands nowhere. The menu is
  // popped through a stand-in, because a native popup under xvfb cannot be read.
  const menuCtor = Menu as unknown as {
    buildFromTemplate: (t: { label?: string }[]) => { popup: (o?: unknown) => void };
  };
  const realBuildMenu = menuCtor.buildFromTemplate;
  let popped: { label?: string }[] = [];
  menuCtor.buildFromTemplate = (t) => {
    popped = t;
    return { popup: () => {} };
  };
  const regMenu = new WebviewRegistry(win, {});
  await regMenu.place('paneEph', 'u1/73', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', false);
  await regMenu.place('paneDur', 'u1/74', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', true);
  regMenu.showMenu('paneEph');
  const ephItems = popped.map((i) => i.label).filter(Boolean);
  regMenu.showMenu('paneDur');
  const durItems = popped.map((i) => i.label).filter(Boolean);
  menuCtor.buildFromTemplate = realBuildMenu;
  if (ephItems.includes('Freeze Page')) fail(`an ephemeral visit offered Freeze Page: ${ephItems.join(',')}`);
  if (!durItems.includes('Freeze Page')) fail(`a durable tile did not offer Freeze Page: ${durItems.join(',')}`);
  await regMenu.remove('paneEph');
  await regMenu.remove('paneDur');
  console.log('canFreeze ok: offered on the durable tile, withheld from the ephemeral visit');

  // ── removeAll clears every pane, and a dead view does not stop it ─────
  // The before-quit flush in index.ts is its only caller, so nothing else
  // reaches it. It runs while Chromium is already tearing windows down, where
  // a view can be gone before its turn: one rejection there would skip the
  // localStorage flush for every other pane and leave the quit to its
  // watchdog.
  const quitErrs: string[] = [];
  const regQ = new WebviewRegistry(win, { onError: (ev) => quitErrs.push(ev.message) });
  const quitPanes = ['paneQ1', 'paneQ2', 'paneQ3'];
  for (let i = 0; i < quitPanes.length; i++) {
    await regQ.place(quitPanes[i], `u1/8${i}`, DATA_URL, { x: i * 200, y: 0, width: 200, height: 150 });
  }
  const quitWcs = quitPanes.map((id) => regQ.webContentsFor(id)!);
  quitWcs[1].close(); // destroyed behind the registry's back, as a quit does
  if (!(await waitFor(() => quitWcs[1].isDestroyed(), 4000))) fail('paneQ2: the view never died');
  let quitRejection = '';
  await regQ.removeAll().catch((err) => {
    quitRejection = String(err);
  });
  if (quitRejection) fail(`removeAll rejected on a dead view: ${quitRejection}`);
  if (regQ.paneIds().length !== 0) fail(`removeAll left ${JSON.stringify(regQ.paneIds())} registered`);
  for (let i = 0; i < quitWcs.length; i++) {
    if (!(await waitFor(() => quitWcs[i].isDestroyed(), 4000))) {
      fail(`removeAll left ${quitPanes[i]}'s view alive: its teardown never ran`);
    }
  }
  if (!quitErrs.some((m) => m.includes('view crashed while closing'))) {
    fail(`removeAll hid the dead view's report (errors: ${JSON.stringify(quitErrs)})`);
  }
  await regQ.removeAll(); // an empty registry resolves rather than throwing
  console.log('removeAll ok: every pane torn down, a dead view neither rejected nor silenced');

  console.log('HARNESS PASS');
  app.exit(0);
});
