// Integration harness for the registry. Boots a real Electron main process,
// creates a window and a WebviewRegistry, hosts a real WebContentsView on a
// data: url, then asserts that capturePage yields non-empty JPEG bytes and that
// a nav event fires. Run with:
//
//   npm run build && xvfb-run -a electron dist/harness/capture-harness.js
//
// Prints "HARNESS PASS" or "HARNESS FAIL: ..." and exits 0 or 1.
import { app, BaseWindow, BrowserWindow, Menu, WebContentsView } from 'electron';
import * as fs from 'node:fs';
import * as http from 'node:http';
import * as os from 'node:os';
import * as path from 'node:path';
import { WebviewRegistry } from '../main/webviews';
import { registerWebviewIpc } from '../main/register';
import type { NavEvent } from '../main/ipc';
import { PARK_COORD, SESSION_PARTITION } from '../main/viewutil';
import { FOCUS_SETTLE_MS } from '../main/focusguard';

// A throwaway Chromium profile per run. The storage-flush scenario asserts what
// is on disk under SESSION_PARTITION, and a previous run's bytes would answer
// for this one; a temp profile also keeps the harness out of the developer's
// own Electron profile. Must be set before app is ready.
const profileDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-harness-'));
app.setPath('userData', profileDir);
process.on('exit', () => {
  try {
    fs.rmSync(profileDir, { recursive: true, force: true });
  } catch {
    // A leftover temp profile is harmless; the run's verdict is already printed.
  }
});

// The touch-scroll scenario synthesizes TouchEvents inside the hosted page.
// Chromium exposes the Touch and TouchEvent constructors only when touch events
// are on. A real touchscreen turns them on by detection; the harness forces them
// so the synthetic gesture can be built. The production app sets no such switch,
// because detection is the product behavior.
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

// startPage serves one tiny html page on loopback. A data: url has an opaque
// origin, so it has no localStorage and no same-document navigation; the
// scenarios that need either need a real origin.
async function startPage(): Promise<{ url: string; close: () => void }> {
  const server = http.createServer((_req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/html' });
    res.end('<title>HarnessPage</title><body>page</body>');
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
  const port = (server.address() as { port: number }).port;
  return { url: `http://127.0.0.1:${port}/`, close: () => server.close() };
}

app.whenReady().then(async () => {
  const navEvents: NavEvent[] = [];
  const win = new BaseWindow({ width: 800, height: 600, show: true });
  const registry = new WebviewRegistry(win, { onNav: (ev) => navEvents.push(ev) });

  await registry.place('pane1', 'u1/42', DATA_URL, { x: 0, y: 0, width: 800, height: 600 });

  const jpeg = await waitForNonEmptyCapture(registry, 'pane1', 6000);
  if (jpeg.length === 0) fail('capturePage produced no frame within 6s');

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
  // The bar's back button and the context menu's Back share one owner,
  // registry.goBack. This pins it against Electron's navigationHistory on a
  // real view: two loads, one goBack, and the first url is current again.
  const FIRST_URL = 'data:text/html,' + encodeURIComponent('<title>First</title>first');
  const SECOND_URL = 'data:text/html,' + encodeURIComponent('<title>Second</title>second');
  await registry.place('paneb', 'u1/46', FIRST_URL, { x: 0, y: 0, width: 400, height: 300 });
  const wcb = registry.webContentsFor('paneb')!;
  const loaded = (url: string) =>
    new Promise<void>((resolve, reject) => {
      const t = setTimeout(() => reject(new Error(`no did-finish-load for ${url.slice(0, 40)}`)), 6000);
      wcb.once('did-finish-load', () => {
        clearTimeout(t);
        resolve();
      });
    });
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
  // PlaceArgs.hidden is the renderer's verdict for this frame. A view placed
  // while the palette is open must land at PARK_COORD, never on top of the
  // canvas overlay for even one round trip, and the next setHidden(false) moves
  // it to its real bounds.
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
  // The crashed-tab ascent: every view-bound read in remove() throws once the
  // webContents is gone, and remove() must still complete and hand back an
  // empty freeze. The wasm side skips the freeze writeback on an all-empty
  // result, so nothing overwrites the good preview.
  // The crash must also be REPORTED: an empty freeze is safe, but the user
  // still needs to know why the tile fell back to its last good preview.
  const deadErrs: string[] = [];
  const reg2 = new WebviewRegistry(win, { onError: (ev) => deadErrs.push(ev.message) });
  await reg2.place('pane2', 'u1/43', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  if ((await waitForNonEmptyCapture(reg2, 'pane2', 6000)).length === 0) {
    fail('dead-view scenario: view produced no frame within 6s');
  }
  // Destroy the renderer out from under the registry: the crashed-tab shape.
  reg2.webContentsFor('pane2')!.close();
  await new Promise((r) => setTimeout(r, 300));
  const deadFreeze = await reg2.remove('pane2');
  if (deadFreeze.jpegBase64 !== '') fail('dead view yielded a non-empty freeze');
  if (!deadErrs.some((m) => m.includes('view crashed while closing'))) {
    fail(`a crash during remove was not reported (errors: ${JSON.stringify(deadErrs)})`);
  }
  console.log('dead-view remove ok: empty freeze, no throw, the crash reported');

  // ── single-finger touch scroll over a live view ─────────────────────────
  // A finger drag over live web content must scroll the page, with the content
  // following the finger: the preload forwards per-move deltas and the registry
  // injects mouseWheel back into the view (see urlview-preload.ts). This crosses
  // the real seam: page TouchEvents, preload listener, IPC through the
  // production registerWebviewIpc wiring, registry.touchScroll, sendInputEvent,
  // and Chromium scrolls. It also pins the delta sign against real Chromium:
  // start mid-page, drag the finger up, and the scroll offset must increase; a
  // flipped convention would decrease it.
  const rootWin = new BrowserWindow({ show: false });
  const reg3 = new WebviewRegistry(win, {});
  registerWebviewIpc(reg3, rootWin.webContents, win);
  const TALL_URL =
    'data:text/html,' + encodeURIComponent('<body style="margin:0;height:20000px">tall</body>');
  await reg3.place('pane3', 'u1/44', TALL_URL, { x: 0, y: 0, width: 800, height: 600 });
  if ((await waitForNonEmptyCapture(reg3, 'pane3', 6000)).length === 0) {
    fail('touch scenario: view produced no frame within 6s');
  }
  const wc3 = reg3.webContentsFor('pane3')!;
  await wc3.executeJavaScript('window.scrollTo(0, 1000)');
  // The preload forwards physical screen coords, and the registry converts them
  // back through getContentBounds and the view bounds. The view sits at content
  // (0,0), so screen equals the content origin plus the client position.
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
  // Focus steal is impossible: only the focused pane's view may hold OS
  // keyboard focus. A pane goes live on paths that are not a gesture on the
  // focused pane — a workspace restore walking every leaf, an ascent
  // re-engaging every content pane, a promote onto another pane's grid — and
  // attaching a WebContentsView and loading a url hands the new widget focus.
  // The renderer owns the fact and carries it on PlaceArgs; the registry must
  // not assume the placement was focused, or the guard returns early and the
  // user's next keystrokes land in a page they never clicked on.
  const steals: string[] = [];
  const regF = new WebviewRegistry(win, { onFocusStolen: (ev) => steals.push(ev.paneId) });
  await regF.place('paneU', 'u1/47', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', false, false, false);
  if (regF.focusedFor('paneU') !== false) {
    fail(`place(focused=false) recorded focused=${String(regF.focusedFor('paneU'))}`);
  }
  const wcU = regF.webContentsFor('paneU')!;
  // Force the grab the guard exists for. Chromium emits 'focus' on the
  // webContents, which is where the guard sits.
  wcU.focus();
  const stealDeadline = Date.now() + 5000;
  while (steals.length === 0 && Date.now() < stealDeadline) {
    await new Promise((r) => setTimeout(r, 100));
  }
  if (steals[0] !== 'paneU') fail('a view placed on an unfocused pane kept OS focus: no steal reported');
  // And the focused case is not bounced: the pane the user acted on keeps its
  // view's focus, or every legitimate descent would fight the guard.
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
  // The guard's hardest case, and the one it got wrong until W2 measured it
  // (docs/debt/w2-native-seam.md §5, M1): Chromium focuses the widget in the
  // browser process WHILE routing the press and forwards the press afterwards,
  // so at the `focus` event nothing about the click has arrived yet — not the
  // browser-process input-event, not the preload's IPC. A guard that decides
  // there reports a steal for the click the user just made.
  //
  // The order below is the measured one: focus first, then the press. The
  // press is a real sendInputEvent, so it raises the same `input-event` on the
  // same webContents that an OS click does; only the widget-focus half is
  // stood in for, because sendInputEvent bypasses Chromium's browser-process
  // focus routing and cannot produce a `focus` event at all.
  const rootView = new WebContentsView({ webPreferences: { sandbox: false } });
  win.contentView.addChildView(rootView);
  rootView.setBounds({ x: 0, y: 0, width: 800, height: 600 });
  await rootView.webContents.loadURL('data:text/html,' + encodeURIComponent('<title>root</title>root'));

  const clickSteals: string[] = [];
  // Wired exactly as index.ts wires it: a reported steal hands OS focus back to
  // the root webContents.
  const regC = new WebviewRegistry(win, {
    onFocusStolen: (ev) => {
      clickSteals.push(ev.paneId);
      rootView.webContents.focus();
    },
  });
  await regC.place('paneC', 'u1/49', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 0, '', false, false, false);
  const wcC = regC.webContentsFor('paneC')!;
  // Let the placement's own grab be bounced and focus land back on the root,
  // so the click below starts from the real precondition: an unfocused live
  // pane whose view does not hold OS focus.
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

  // Park the view of a fresh steal-scenario registry on an unfocused pane with
  // OS focus sitting on the root, which is the precondition every steal starts
  // from. The placement's own grab is bounced first, so the recorder starts
  // empty and only the deliberate grab below counts.
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
  // Chromium's widget-focus commit can land after the bounce with no further
  // focus event, so the guard confirms once at FOCUS_SETTLE_MS. Nothing here
  // hands focus back, so the view still holds it at the confirmation and the
  // second bounce fires. It must then stop: a chain would report a steal every
  // 120 ms forever for a view the app has given up on.
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
  // remove() cancels the timer, but that only covers a teardown that went
  // through the registry. A render-process crash or a host-side close leaves
  // the timer armed over a destroyed WebContents, where every read throws —
  // uncaught inside a timer, which hangs main behind an error dialog. The
  // scenario the code's comment warns about, and until now untested. Verified
  // against the unguarded code: it does not report a phantom steal, it wedges,
  // and this harness never reaches HARNESS PASS.
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
  // The renderer closes a pane's live view before placing another, and never
  // re-places the tile already live there, so this branch is a renderer bug.
  // What it must not be is silent: the replaced view's freeze has no caller to
  // land in, so its final frame — the tile's preview — is lost, and the only
  // evidence is this report.
  const replaceErrs: string[] = [];
  const regR = new WebviewRegistry(win, { onError: (ev) => replaceErrs.push(ev.message) });
  await regR.place('paneR', 'u1/60', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  await regR.place('paneR', 'u1/61', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  if (replaceErrs.length !== 1) fail(`a replaced live view reported ${replaceErrs.length} errors, want 1`);
  if (!replaceErrs[0].includes('u1/60 → u1/61')) fail(`the replace report does not name both tiles: ${replaceErrs[0]}`);
  // The replacement stands and the old view is gone: one entry, the new tile.
  if (regR.paneIds().length !== 1) fail(`the replace left ${regR.paneIds().length} entries for one pane`);
  if (regR.tileIdFor('paneR') !== 'u1/61') fail(`the replacement did not take (tile ${regR.tileIdFor('paneR')})`);
  await regR.remove('paneR');
  console.log('place replace ok: reported, old view torn down, replacement stands');

  // ── setBounds returns early when nothing moved ──────────────────────────
  // syncURLViews calls setBounds every frame, so the equal-bounds early return
  // is the difference between a no-op and re-applying the composed zoom sixty
  // times a second. Zoom is what makes it observable: the registry only touches
  // zoomFactor through applyMinWidthZoom, so moving it out from under the
  // registry and calling setBounds with the same bounds must leave it alone,
  // while a real bounds change must recompute it.
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
  // every frame. A focus-only change must still be recorded — it is the fact
  // the steal guard reads — and must not move the view: the pane did not
  // become hidden, so parking it would blank live content on a mere focus move.
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
  // The failure the file's own comment forbids silently — a live view left
  // sitting on top of the pane the user just ascended out of. Forced by
  // destroying the window under the registry, which is what a host-side window
  // close does: removeChildView then throws, and remove() must still complete
  // and say why a blank rectangle is covering a pane.
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
  // The largest documented-but-untested claim in webviews.ts: Chromium flushes
  // localStorage lazily, so an abrupt close can drop a site's unsubmitted
  // draft. The claim is about bytes on disk, so that is what this reads — the
  // partition's own leveldb, before and after the remove. A data: url has an
  // opaque origin and no storage at all, hence the loopback server.
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
  // The timer's closure holds the view, and firing after webContents.close()
  // would throw uncaught in main. The cancel is invisible from outside, so the
  // scenario watches the timer itself: every FOCUS_SETTLE_MS timeout the
  // registry arms is recorded, and the one still pending at the remove must be
  // the one cleared.
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
  // focus, so that handler never sees the key; the registry mirrors it. The
  // host window is a stand-in here on purpose: real fullscreen needs a window
  // manager, and there is none under xvfb, so asserting isFullScreen() on a
  // real BaseWindow would test the display server. What must be pinned is the
  // relay — the key reaches the window's own toggle — and both directions of it.
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
  // did-navigate fires for a document load; a hash change or a pushState fires
  // did-navigate-in-page only. Both change the tile's address, and the
  // renderer's cached address is what the bar shows and what a freeze
  // persists, so both must emit. A data: url cannot do either — its origin is
  // opaque — hence the loopback page.
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
  // Unreported, render-process-gone leaves the view blank with no signal at
  // all. The message names the page, which means getURL() is read on a
  // webContents whose renderer has just died — the read the code wraps in a
  // try/catch so a throw there cannot swallow the notice.
  const crashErrs: string[] = [];
  const regP = new WebviewRegistry(win, { onError: (ev) => crashErrs.push(ev.message) });
  await regP.place('paneP', 'u1/70', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  if ((await waitForNonEmptyCapture(regP, 'paneP', 6000)).length === 0) fail('crash scenario: no frame within 6s');
  regP.webContentsFor('paneP')!.forcefullyCrashRenderer();
  if (!(await waitFor(() => crashErrs.some((m) => m.startsWith('page crashed')), 6000))) {
    fail(`a crashed renderer was not reported (errors: ${JSON.stringify(crashErrs)})`);
  }
  const crashMsg = crashErrs.find((m) => m.startsWith('page crashed'))!;
  if (!crashMsg.includes('data:text/html')) fail(`the crash report does not name the page: ${crashMsg}`);
  await regP.remove('paneP');
  console.log('render-process-gone ok: the crash is reported and names the page');

  // ── the min-width zoom survives a navigation ────────────────────────────
  // zoomFactor is per-document: Chromium resets it across a navigation, so a
  // narrow pane would reflow to a cramped mobile layout on the second page
  // unless did-finish-load re-applies the composed factor.
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
  // A frozen mirror is otherwise evidence-free: the pane shows a stale frame
  // and nothing anywhere says why. The report must fire on the transition into
  // failure and NOT once per captured frame — the pump captures every live pane
  // on a timer, so a per-frame report would bury the log.
  const capErrs: string[] = [];
  const regM = new WebviewRegistry(win, { onError: (ev) => capErrs.push(ev.message) });
  await regM.place('paneM', 'u1/72', DATA_URL, { x: 0, y: 0, width: 400, height: 300 });
  if ((await waitForNonEmptyCapture(regM, 'paneM', 6000)).length === 0) fail('mirror scenario: no frame within 6s');
  regM.webContentsFor('paneM')!.close(); // destroyed behind the registry's back
  await new Promise((r) => setTimeout(r, 300));
  if ((await regM.capture('paneM')) !== '') fail('a capture of a destroyed view returned a frame');
  if ((await regM.capture('paneM')) !== '') fail('the second capture of a destroyed view returned a frame');
  const failing = capErrs.filter((m) => m.includes('mirror capture failing'));
  if (failing.length !== 1) fail(`a failing capture streak reported ${failing.length} times, want 1`);
  await regM.remove('paneM');
  console.log('capture streak ok: the failure is reported once, not per frame');

  // ── Freeze Page appears only where there is something to freeze ─────────
  // canFreeze is the registry's half of the gating: contextmenu.test.ts owns
  // the template's arms, and url-circle-menu.spec.ts the durable positive. What
  // was uncovered is the registry reading `durable` off the entry — an
  // ephemeral visit has no tile to re-descend into, so offering the item would
  // promise a freeze that lands nowhere. The menu is built for real and popped
  // through a stand-in, because a native popup under xvfb cannot be read back.
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

  console.log('HARNESS PASS');
  app.exit(0);
});
