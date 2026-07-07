import { BaseWindow, WebContentsView, Menu, clipboard, session } from 'electron';
import type { ContextMenuParams, MenuItemConstructorOptions } from 'electron';
import * as path from 'node:path';
import type { Bounds, FreezeResult, NavEvent, ErrorEvent } from './ipc';
import {
  SESSION_PARTITION,
  partitionFor,
  roundBounds,
  boundsEqual,
  controlVisible,
  controlBounds,
  parkedBounds,
  minWidthZoomFactor,
  URL_MIN_LAYOUT_WIDTH,
  shouldSurfaceFailLoad,
  failLoadMessage,
  renderProcessGoneMessage,
  proxyRulesFor,
} from './viewutil';
import { urlContextMenuTemplate } from './contextmenu';
import { hydratePartition, dehydratePartition } from './session';
import { captureJpegBase64 } from './capture';

// urlViewPreload is the script injected into every live URL view; it forwards
// a right-button press to main so the renderer can gesture over live content.
// __dirname is dist/main at runtime, so the compiled preload sits one level up.
const urlViewPreload = path.join(__dirname, '..', 'preload', 'urlview-preload.js');

interface Entry {
  view: WebContentsView;
  // control is a small native button view floated on TOP of `view` at the
  // corner. A canvas-drawn button can't paint above a native WebContentsView,
  // so the corner control (back / ascend) is itself a tiny native view.
  control: WebContentsView;
  tileId: number;
  objectId: string;
  bounds: Bounds;
  hidden: boolean;
  // focused is whether this pane is the focused pane. The corner control shows
  // only on the focused pane (controlVisible) — unfocused live panes keep their
  // content but drop the circle, so the menu/ascend handle is on exactly one
  // pane at a time.
  focused: boolean;
  // partition is the Electron session partition this view is bound to — the
  // owning plugin's (persist:plugin-<uuid>). A pane re-targeted at a tile in a
  // different plugin must tear down rather than cross sessions.
  partition: string;
  // pluginUuid owns the tile (the session boundary); used to dehydrate the
  // session back to the plugin DB on ascent.
  pluginUuid: string;
}

// CONTROL_SIZE / CONTROL_MARGIN place the corner button at the bottom-right
// of the URL view, matching the canvas circle's position on frozen panes.
const CONTROL_SIZE = 36;
const CONTROL_MARGIN = 6;

// CONTROL_HTML is the corner button's page: a circular back-arrow chip. Its
// inline script (nodeIntegration — first-party data: URL only) forwards
// left/right mousedown to main, which routes left→back and right→ascend.
const CONTROL_HTML =
  'data:text/html,' +
  encodeURIComponent(
    `<!doctype html><meta charset=utf8><style>
     html,body{margin:0;height:100%;overflow:hidden;-webkit-user-select:none;
       background:transparent;display:flex;align-items:center;justify-content:center}
     #b{width:30px;height:30px;border-radius:50%;background:#1b1f29;
       border:1px solid #3a4150;color:#cdd2dd;display:flex;align-items:center;
       justify-content:center;font:18px/1 sans-serif;cursor:pointer}
     #b:hover{background:#252b38}</style>
     <div id=b>‹</div>
     <script>
     const {ipcRenderer}=require('electron');
     addEventListener('mousedown',e=>{e.preventDefault();
       ipcRenderer.send('gw:control-click',e.button)});
     addEventListener('contextmenu',e=>e.preventDefault());
     </script>`,
  );

// The corner control's geometry and the min-layout width are view config; the
// pure placement/park/zoom math lives in viewutil (controlBounds, parkedBounds,
// minWidthZoomFactor) where it is unit-tested.

export interface RegistryCallbacks {
  // onNav fires when a hosted view finishes a navigation (URL/title change),
  // so the renderer can update the cached tile address.
  onNav?: (ev: NavEvent) => void;
  // onError fires for every webview/session failure the registry detects —
  // did-fail-load, render-process-gone, a crash during remove(), a session
  // hydrate/dehydrate failure. index.ts wires this to sendError(rootWC, ...),
  // which is the ONE path onto EV.error (issue #46). The registry itself
  // stays free of IPC knowledge — it only reports; index.ts decides how the
  // report reaches the renderer.
  onError?: (ev: ErrorEvent) => void;
}

// WebviewRegistry owns the live URL-tile WebContentsViews parented to the
// root window. One view per paneId; each view is bound to its owning plugin's
// persistent partition (persist:plugin-<uuid>), so a plugin's url tiles act
// like tabs of one browser (shared cookies/logins and DOM storage) isolated
// from other plugins'. The registry is deliberately free of IPC
// and store knowledge — ipc.ts wires Electron handlers to these methods, and
// the renderer remains the only thing that talks to the Go backend.
export class WebviewRegistry {
  private readonly win: BaseWindow;
  private readonly cb: RegistryCallbacks;
  private readonly origin: string;
  private readonly entries = new Map<string, Entry>();
  // hydrated tracks which per-plugin partitions have had their session pulled
  // down from the plugin DB this run, so we hydrate each at most once.
  private readonly hydrated = new Set<string>();
  // _globalHidden is the registry's single copy of "are all views currently
  // parked for a gesture/modal" — the last value seen by setHidden. A new view
  // placed while this is true starts parked rather than landing over an open
  // palette or drag ghost. Convergent: setHidden always re-applies the correct
  // state after place, but this closes the window where a briefly-visible new
  // view could occlude a canvas overlay.
  private _globalHidden = false;

  constructor(win: BaseWindow, origin: string, cb: RegistryCallbacks = {}) {
    this.win = win;
    this.origin = origin;
    this.cb = cb;
  }

  // toggleFullScreen flips the host window's fullscreen state. Used by the
  // F11 handler injected into live URL views, which would otherwise swallow
  // the key (the canvas's own F11 handler can't see it while a native view
  // has focus).
  private toggleFullScreen(): void {
    this.win.setFullScreen(!this.win.isFullScreen());
  }

  // showContextMenu builds and pops the live URL view's right-click menu. The
  // policy (which items, what each does) lives in the pure urlContextMenuTemplate
  // (unit-tested); here we only translate Electron's params + bind the actions
  // to the real clipboard and webContents, then pop the menu over the window.
  private showContextMenu(view: WebContentsView, params: ContextMenuParams): void {
    const wc = view.webContents;
    const nav = wc.navigationHistory;
    const template = urlContextMenuTemplate(
      {
        linkURL: params.linkURL,
        selectionText: params.selectionText,
        isEditable: params.isEditable,
        editFlags: {
          canCut: params.editFlags.canCut,
          canCopy: params.editFlags.canCopy,
          canPaste: params.editFlags.canPaste,
        },
        canGoBack: nav.canGoBack(),
        canGoForward: nav.canGoForward(),
      },
      {
        copyText: (t) => clipboard.writeText(t),
        copyLink: (u) => clipboard.writeText(u),
        openLink: (u) => void wc.loadURL(u),
        cut: () => wc.cut(),
        paste: () => wc.paste(),
        back: () => {
          if (nav.canGoBack()) nav.goBack();
        },
        forward: () => {
          if (nav.canGoForward()) nav.goForward();
        },
        reload: () => wc.reload(),
      },
    );
    const menu = Menu.buildFromTemplate(template as MenuItemConstructorOptions[]);
    menu.popup({ window: this.win });
  }

  has(paneId: string): boolean {
    return this.entries.has(paneId);
  }

  paneIds(): string[] {
    return [...this.entries.keys()];
  }

  // controlStateFor is a test-only read of a pane's corner-control state: the
  // focused/hidden flags the renderer fed in, and whether the control is
  // therefore on screen (controlVisible). Lets an e2e prove the focused-pane rule
  // (I9) end to end — that a live URL pane's corner control hides when its pane
  // loses focus (the owner's "the circle is still visible on url panes when not
  // focused" report) — by reading the actual fed state, not just the predicate.
  controlStateFor(paneId: string): { focused: boolean; hidden: boolean; visible: boolean } | undefined {
    const e = this.entries.get(paneId);
    if (!e) return undefined;
    return { focused: e.focused, hidden: e.hidden, visible: controlVisible(e.hidden, e.focused) };
  }

  // tileIdFor returns the tile id hosted in paneId, or undefined.
  tileIdFor(paneId: string): number | undefined {
    return this.entries.get(paneId)?.tileId;
  }

  // viewBoundsFor is a test-only accessor that returns the view's actual
  // physical bounds as Electron last set them, revealing whether the view is
  // currently parked or at its intended (visible) position. Used by e2e to
  // assert that place-while-hidden does NOT lift the view out of its parked
  // position. Returns undefined if the pane has no entry.
  viewBoundsFor(paneId: string): { x: number; y: number; width: number; height: number } | undefined {
    const e = this.entries.get(paneId);
    if (!e) return undefined;
    // View inherits getBounds() from Electron's View base class.
    return (e.view as unknown as { getBounds(): { x: number; y: number; width: number; height: number } }).getBounds();
  }

  // place creates (or re-targets) the view for paneId. If a view already
  // exists for the pane it's reused; a URL change re-navigates it. The view
  // is added as a child of the window's contentView, so it paints above the
  // root canvas renderer at the given bounds.
  async place(paneId: string, tileId: number, objectId: string, url: string, bounds: Bounds, pluginUuid: string, proxyEndpoint = ''): Promise<void> {
    const rounded = roundBounds(bounds);
    const partition = partitionFor(pluginUuid);
    let e = this.entries.get(paneId);

    if (e && (e.objectId !== objectId || e.partition !== partition)) {
      // Different tile (or a tile in a different plugin → different session) in
      // the same pane — tear the old view down first so we don't leak a view or
      // cross sessions.
      this.remove(paneId).catch(() => {});
      e = undefined;
    }

    // Pull the plugin's session down into its partition before the first view
    // for it loads, so url tiles open already logged in — and point the
    // partition at the grid-stamped proxy (a remote plugin's tiles browse
    // through the tunnel SOCKS, exiting on the remote's network). Once per
    // partition; the endpoint is stable for a plugin's lifetime (the ssh
    // process mints it at spawn).
    if (pluginUuid && !this.hydrated.has(partition)) {
      this.hydrated.add(partition);
      const rules = proxyRulesFor(proxyEndpoint);
      if (rules) {
        await session.fromPartition(partition).setProxy({ proxyRules: rules });
      }
      await hydratePartition(this.origin, pluginUuid, (message) =>
        this.cb.onError?.({ source: 'electron:session', message }),
      );
    }

    if (!e) {
      const view = new WebContentsView({
        webPreferences: {
          partition,
          contextIsolation: true,
          nodeIntegration: false,
          // Forwards a right-button press to main → renderer so pane gestures
          // work over live content. Safe on arbitrary pages: it only listens
          // for button 2 and uses ipcRenderer, nothing else.
          preload: urlViewPreload,
        },
      });
      // The corner button is its own tiny native view layered ON TOP of the
      // URL view (added after it). First-party data: URL, so nodeIntegration
      // is acceptable for its inline IPC forwarder.
      const control = new WebContentsView({
        webPreferences: { nodeIntegration: true, contextIsolation: false, sandbox: false },
      });
      // target=_blank / window.open: follow the link in this same view
      // instead of letting Electron spawn a detached BrowserWindow. A
      // same-view navigation is an ordinary click as far as the server is
      // concerned (real Chromium, the tile's persistent session), so it
      // avoids the bot-guard friction a popup window tends to trip.
      view.webContents.setWindowOpenHandler(({ url: target }) => {
        if (target && target !== 'about:blank') void view.webContents.loadURL(target);
        return { action: 'deny' };
      });
      // F11 fullscreen: the canvas handles F11 via window.ts, but a focused
      // live URL view owns OS keyboard focus, so that handler never sees the
      // key. Mirror it here so fullscreen toggles no matter which view is
      // focused.
      view.webContents.on('before-input-event', (event, input) => {
        if (input.type === 'keyDown' && input.key === 'F11') {
          this.toggleFullScreen();
          event.preventDefault();
        }
      });
      // A plain right-click over live content must show a context menu (copy
      // link, copy, back, …). Electron's WebContentsView has NO default menu —
      // it only emits this event and leaves the menu to us. The injected
      // preload already suppresses this event for a right-DRAG (a pane
      // gesture), so reaching here means a genuine click.
      view.webContents.on('context-menu', (_event, params) => this.showContextMenu(view, params));
      // focused starts true: a pane only goes live by an action on the focused
      // pane, so the control should appear immediately; syncURLViews corrects
      // it on the next frame if focus has already moved.
      // hidden starts from _globalHidden so a view placed while the palette is
      // open (or during a drag gesture) starts parked rather than landing on top
      // of the canvas overlay. syncURLViews will call setHidden for this pane on
      // the next draw() and reaffirm the correct state.
      const startHidden = this._globalHidden;
      e = { view, control, tileId, objectId, bounds: rounded, hidden: startHidden, focused: true, partition, pluginUuid };
      this.entries.set(paneId, e);
      this.win.contentView.addChildView(view);
      view.setBounds(startHidden ? parkedBounds(rounded.width, rounded.height) : rounded);
      this.win.contentView.addChildView(control);
      control.setBackgroundColor('#00000000');
      this.applyControlBounds(e);
      void control.webContents.loadURL(CONTROL_HTML);
      this.wireNav(paneId, e);
      this.applyMinWidthZoom(e);
      void view.webContents.loadURL(url);
      return;
    }

    // Reuse: update the stored bounds and, if visible, apply them immediately.
    // When hidden (parked for a gesture/palette), only update e.bounds so that
    // setHidden(false) will un-park to the NEW position — never call
    // view.setBounds while hidden, because that would physically lift the view
    // out of its parked position over the canvas overlay, and the next setHidden
    // call would no-op (e.hidden is still true, nothing "changed"). This is the
    // primary root cause of the "palette appears under a live URL view" bug:
    // place() was re-asserting the view on top every time bounds changed.
    e.tileId = tileId;
    if (!boundsEqual(e.bounds, rounded)) {
      e.bounds = rounded;
      if (!e.hidden) {
        e.view.setBounds(rounded);
        this.applyControlBounds(e);
      }
    }
    const current = e.view.webContents.getURL();
    if (current !== url && url) {
      void e.view.webContents.loadURL(url);
    }
  }

  setBounds(paneId: string, bounds: Bounds): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    const rounded = roundBounds(bounds);
    if (boundsEqual(e.bounds, rounded)) return;
    e.bounds = rounded;
    if (!e.hidden) {
      e.view.setBounds(rounded);
      this.applyControlBounds(e);
    }
    this.applyMinWidthZoom(e);
  }

  // applyControlBounds places the corner control at the view's bottom-right, or
  // parks it off-screen when it shouldn't show (unfocused pane, or the whole
  // view parked for a gesture). One source of truth so the control's on-screen
  // state can't drift from controlVisible.
  private applyControlBounds(e: Entry): void {
    if (controlVisible(e.hidden, e.focused)) {
      e.control.setBounds(roundBounds(controlBounds(e.bounds, CONTROL_SIZE, CONTROL_MARGIN)));
    } else {
      e.control.setBounds(parkedBounds(CONTROL_SIZE, CONTROL_SIZE));
    }
  }

  // controlPaneFor resolves a control view's webContents id back to its pane,
  // so the IPC handler knows which tile a corner-button click came from.
  controlPaneFor(webContentsId: number): string | undefined {
    for (const [paneId, e] of this.entries) {
      if (e.control.webContents.id === webContentsId) return paneId;
    }
    return undefined;
  }

  // applyMinWidthZoom keeps a narrow URL pane from reflowing the page to a
  // cramped (mobile) layout: below URL_MIN_LAYOUT_WIDTH we zoom the page out
  // so it still lays out at the min width and scales to fit, instead of
  // re-flowing. A native WebContentsView can't render wider than its bounds
  // and be clipped to the pane, so this scale-to-fit is the closest thing to
  // "min width + horizontal scroll" without offscreen rendering. zoomFactor
  // resets on cross-origin navigation, so wireNav re-applies it on load.
  private applyMinWidthZoom(e: Entry): void {
    const z = minWidthZoomFactor(e.bounds.width, URL_MIN_LAYOUT_WIDTH);
    try {
      e.view.webContents.setZoomFactor(z);
    } catch {
      // webContents not ready yet — wireNav re-applies on did-finish-load.
    }
  }

  // setHidden hides/shows the view without destroying it, and tracks whether
  // the pane is focused. `hidden` parks the whole view off-screen during drag
  // gestures and modals so canvas-drawn overlays (palette, ghosts) can paint
  // where the native view would otherwise sit on top. `focused` drives only the
  // corner control: an unfocused live pane keeps its content on screen but
  // hides its circle, so exactly one pane shows the control at a time. Called
  // every frame from syncURLViews, so it no-ops when nothing changed.
  setHidden(paneId: string, hidden: boolean, focused: boolean): void {
    // Track the registry-level hidden state so place() can initialize new
    // views correctly (see _globalHidden). We update it regardless of whether
    // the entry exists — the caller (syncURLViews) passes the same hidden value
    // for all on-grid panes, so the last value written is authoritative.
    this._globalHidden = hidden;
    const e = this.entries.get(paneId);
    if (!e || (e.hidden === hidden && e.focused === focused)) return;
    const viewChanged = e.hidden !== hidden;
    e.hidden = hidden;
    e.focused = focused;
    if (viewChanged) {
      if (hidden) {
        e.view.setBounds(parkedBounds(e.bounds.width, e.bounds.height));
      } else {
        e.view.setBounds(e.bounds);
      }
    }
    this.applyControlBounds(e);
  }

  // remove captures a final frame + the page's URL/title, detaches and
  // destroys the view, and returns the freeze payload for persistence.
  async remove(paneId: string): Promise<FreezeResult> {
    const e = this.entries.get(paneId);
    if (!e) return { jpegBase64: '', url: '', title: '' };
    this.entries.delete(paneId);

    let jpegBase64 = '';
    let url = '';
    let title = '';
    try {
      url = e.view.webContents.getURL();
      title = e.view.webContents.getTitle();
      // Commit DOM storage (localStorage) to the shared persistent partition
      // BEFORE the renderer is closed. Chromium writes cookies eagerly but
      // flushes localStorage lazily, so an abrupt webContents.close() can drop
      // recent localStorage writes — which is exactly where GitLab autosaves
      // an unsubmitted comment draft. Flushing here is what makes that draft
      // survive ascend → descend → go-live. Cheap and best-effort; the session
      // outlives the view, so the write lands even though the view goes away.
      e.view.webContents.session.flushStorageData();
      // Capture the plugin's session back to its DB (the system of record) on
      // ascent — fire-and-forget so teardown isn't blocked on the network.
      void dehydratePartition(this.origin, e.pluginUuid, (message) =>
        this.cb.onError?.({ source: 'electron:session', message }),
      );
      jpegBase64 = await captureJpegBase64(e.view);
    } catch {
      // Best-effort: a crashed/destroyed view yields an empty freeze. This is
      // audited issue #46 point 5 — VERIFIED, not assumed: the wasm-side guard
      // (bridgeRemove in client/wasm/url_stream_client.go: `if len(jpeg)>0 ||
      // url!="" || title!=""`) already skips SetURLState entirely when all
      // three come back empty, so an empty freeze here does NOT overwrite a
      // good preview with a blank one — the audit's speculative "crash blanks
      // the preview" failure mode does not exist in this code. What DOES need
      // to surface is the crash itself, so the user knows why the tile fell
      // back to its last good preview instead of the page just "disappearing".
      this.cb.onError?.({
        source: 'electron:webview',
        message: 'view crashed while closing — preview not updated',
      });
    } finally {
      // Detach + free the view no matter what the capture did. This MUST
      // run even if the capture above threw or timed out: the renderer has
      // already dropped this pane from its live set, so a view left attached
      // here would sit blank on top of the pane the user just ascended out
      // of, while every other pane shows the frozen preview fine.
      try {
        this.win.contentView.removeChildView(e.view);
        e.view.webContents.close();
        this.win.contentView.removeChildView(e.control);
        e.control.webContents.close();
      } catch {
        // ignore
      }
    }
    return { jpegBase64, url, title };
  }

  // capture grabs a current frame for mirroring to other panes, without
  // tearing the view down. Returns '' if the pane has no live view.
  async capture(paneId: string): Promise<string> {
    const e = this.entries.get(paneId);
    if (!e || e.hidden) return '';
    try {
      return await captureJpegBase64(e.view);
    } catch {
      return '';
    }
  }

  goBack(paneId: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    const wc = e.view.webContents;
    // navigationHistory is the modern API; fall back to canGoBack/goBack.
    const nav = (wc as unknown as { navigationHistory?: { canGoBack(): boolean; goBack(): void } }).navigationHistory;
    if (nav) {
      if (nav.canGoBack()) nav.goBack();
    } else if ((wc as unknown as { canGoBack?: () => boolean }).canGoBack?.()) {
      (wc as unknown as { goBack: () => void }).goBack();
    }
  }

  reload(paneId: string): void {
    this.entries.get(paneId)?.view.webContents.reload();
  }

  // removeAll tears everything down (app quit / window close).
  async removeAll(): Promise<void> {
    await Promise.all(this.paneIds().map((id) => this.remove(id)));
  }

  private wireNav(paneId: string, e: Entry): void {
    const emit = () => {
      this.cb.onNav?.({
        paneId,
        tileId: e.tileId,
        url: e.view.webContents.getURL(),
        title: e.view.webContents.getTitle(),
      });
    };
    e.view.webContents.on('did-navigate', emit);
    e.view.webContents.on('did-navigate-in-page', emit);
    e.view.webContents.on('page-title-updated', emit);
    // zoomFactor resets across (cross-origin) navigations — re-apply the
    // min-width zoom once the new document has loaded.
    e.view.webContents.on('did-finish-load', () => this.applyMinWidthZoom(e));

    // did-fail-load was previously unhandled entirely (issue #46 point 3): a
    // live URL view could go blank with zero signal to the user. Chromium also
    // fires this constantly for benign reasons — shouldSurfaceFailLoad filters
    // those out (a cancelled/superseded navigation, and any subframe failure)
    // so only a genuine main-frame failure reaches the user.
    e.view.webContents.on(
      'did-fail-load',
      (_event, errorCode, errorDescription, validatedURL, isMainFrame) => {
        if (!shouldSurfaceFailLoad(errorCode, isMainFrame)) return;
        this.cb.onError?.({
          source: 'electron:webview',
          message: failLoadMessage(validatedURL, errorDescription, errorCode),
        });
      },
    );

    // render-process-gone (the renderer process crashed, e.g. an OOM or a GPU
    // crash) was also unhandled anywhere (issue #46 point 3) — the view just
    // sat blank. getURL() after a crash is best-effort; a throw here must not
    // stop the notice from being reported.
    e.view.webContents.on('render-process-gone', (_event, details) => {
      let url = '';
      try {
        url = e.view.webContents.getURL();
      } catch {
        // best-effort; renderProcessGoneMessage handles an empty url cleanly
      }
      this.cb.onError?.({
        source: 'electron:webview',
        message: renderProcessGoneMessage(url, details.reason),
      });
    });
  }
}

// clearSharedSession wipes the shared cookies/storage for ALL tiles. Not
// used yet (a future "sign out" gesture); since the session is shared, this
// logs every tile out at once — there is no per-tile session to clear.
export async function clearSharedSession(): Promise<void> {
  await session.fromPartition(SESSION_PARTITION).clearStorageData();
}
