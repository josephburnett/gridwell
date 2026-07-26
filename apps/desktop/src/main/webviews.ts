import { BaseWindow, WebContentsView, Menu, clipboard, session, WebContents } from 'electron';
import type { ContextMenuParams, MenuItemConstructorOptions } from 'electron';
import * as path from 'node:path';
import type { Bounds, FreezeResult, NavEvent, ErrorEvent, OpenBelowEvent, ZoomKeyEvent } from './ipc';
import {
  SESSION_PARTITION,
  roundBounds,
  boundsEqual,
  controlVisible,
  controlBounds,
  parkedBounds,
  namePillBounds,
  minWidthZoomFactor,
  composeZoom,
  serializeHistory,
  parseHistory,
  URL_MIN_LAYOUT_WIDTH,
  shouldSurfaceFailLoad,
  failLoadMessage,
  renderProcessGoneMessage,
  proxyRulesFor,
  cookieDomainMatches,
  storageOriginsFor,
  zoomChordKey,
} from './viewutil';
import { urlContextMenuTemplate } from './contextmenu';
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
  // userZoom is the tile's persisted content zoom (issue #82); composed with
  // the min-width layout zoom in applyMinWidthZoom. 0 = unset (1.0).
  userZoom: number;
  // namePill is the native name-bubble twin at the view's top-center — DOM
  // cannot paint above a WebContentsView, so live panes get this instead of
  // the renderer's #gw-rename-pill (issue #118). Shows with focus, like the
  // corner control. nameLabel is the entry's OWNED copy of the text: pushed
  // on change and re-pushed when the pill page finishes loading, so a push
  // that raced the load is never lost.
  namePill: WebContentsView;
  nameLabel: string;
  // lastUserClickMs is when the view's preload last forwarded a left press —
  // the one legitimate way a view acquires OS focus (issue #172). The focus
  // guard treats a grab inside this grace window as user intent.
  lastUserClickMs: number;
}

// CONTROL_SIZE / CONTROL_MARGIN place the corner button at the bottom-right
// of the URL view, matching the canvas circle's position on frozen panes.
const CONTROL_SIZE = 36;

// USER_CLICK_FOCUS_GRACE_MS is how long after a forwarded left press a view
// may legitimately acquire OS focus (issue #172): the native focus lands
// immediately on press, while the wasm marks the pane focused a round trip
// later — the stamp bridges that gap. Long enough for a slow frame, far
// shorter than any refresh cadence worth stealing for.
const USER_CLICK_FOCUS_GRACE_MS = 1500;

// FOCUS_RECHECK_MS is the settle delay before the steal guard double-checks:
// long enough for an in-flight widget-focus commit to land, short enough
// that leaked keystrokes stay negligible.
const FOCUS_RECHECK_MS = 120;
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

// NAME_HTML is the native name-bubble twin for LIVE url panes (issue #118):
// DOM cannot paint above a WebContentsView, so the focused pane's bubble is
// itself a tiny native view at the top-center. window.setLabel(text) updates
// it; mousedown forwards the button to main (left → open the rename input in
// the renderer, right → pane zoom), mirroring the corner control's pattern.
const NAME_HTML =
  'data:text/html,' +
  encodeURIComponent(
    `<!doctype html><meta charset=utf8><style>
     html,body{margin:0;height:100%;overflow:hidden;-webkit-user-select:none;
       background:transparent;display:flex;align-items:flex-start;justify-content:center}
     #p{max-width:100%;padding:1px 10px;border-radius:10px;background:#23252d;
       border:1px solid #1f2229;color:#c8c9ce;font:12px sans-serif;cursor:pointer;
       overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
     #p:hover{background:#2d3140}</style>
     <div id=p></div>
     <script>
     const {ipcRenderer}=require('electron');
     window.setLabel=t=>{document.getElementById('p').textContent=t};
     addEventListener('mousedown',e=>{e.preventDefault();
       ipcRenderer.send('gw:name-click',e.button)});
     addEventListener('contextmenu',e=>e.preventDefault());
     </script>`,
  );

// NAME_PILL_* size the native bubble strip centered at the view's top.
const NAME_PILL_W = 240;
const NAME_PILL_H = 24;
const NAME_PILL_MARGIN = 4;

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
  // onOpenBelow fires when a hosted view's page tries to open a NEW WINDOW
  // (target=_blank, window.open, ctrl/cmd-click). The renderer splits the
  // pane and opens the url as an ephemeral visit below (issue #111).
  onOpenBelow?: (ev: OpenBelowEvent) => void;
  // onZoomKey fires when the content-zoom chord (Ctrl/Cmd +/=/-/0) is pressed
  // while this view owns OS keyboard focus (issue #170). The renderer's
  // applyContentZoom — the one owner of cache + persistence — handles it.
  onZoomKey?: (ev: ZoomKeyEvent) => void;
  // onFocusStolen fires when a live view acquired OS keyboard focus WITHOUT
  // the user acting on its pane — a page-initiated navigation makes Chromium
  // focus the new document's widget (issue #172). index.ts hands focus back
  // to the root window's webContents, where the canvas and every shell
  // overlay live.
  onFocusStolen?: (ev: { paneId: string }) => void;
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
        pageHost: (() => {
          try {
            return new URL(wc.getURL()).hostname;
          } catch {
            return '';
          }
        })(),
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
        clearSiteData: () => void this.clearSiteData(wc),
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
  async place(paneId: string, tileId: number, objectId: string, url: string, bounds: Bounds, contentZoom = 0, history = '', nameLabel = ''): Promise<void> {
    const rounded = roundBounds(bounds);
    // ONE host-local session (owner decision 2026-07-26): every live url
    // tile, local or through a mount, browses on the shared persistent
    // partition — your own logins everywhere. The per-plugin partitions and
    // their hydrate/dehydrate choreography are gone.
    const partition = SESSION_PARTITION;
    let e = this.entries.get(paneId);

    if (e && e.objectId !== objectId) {
      // A different tile in the same pane — tear the old view down first so
      // we don't leak a view.
      this.remove(paneId).catch(() => {});
      e = undefined;
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
      // target=_blank / window.open / ctrl-click: everything Chromium would
      // open as a new window or tab arrives here. Never spawn a detached
      // BrowserWindow — instead hand the url to the renderer, which splits
      // the pane and opens it as an EPHEMERAL visit in the lower half
      // (issue #111): the link opens in real Chromium on the tile's
      // persistent session (no popup bot-guard friction), in a pane you can
      // read next to the page you came from, and it dies on ascent.
      view.webContents.setWindowOpenHandler(({ url: target }) => {
        if (target && target !== 'about:blank') {
          this.cb.onOpenBelow?.({ paneId, url: target });
        }
        return { action: 'deny' };
      });
      // F11 fullscreen: the canvas handles F11 via window.ts, but a focused
      // live URL view owns OS keyboard focus, so that handler never sees the
      // key. Mirror it here so fullscreen toggles no matter which view is
      // focused.
      // The content-zoom chord gets the same treatment as F11 (issue #170):
      // intercepted here and relayed to the renderer, where the ONE zoom
      // owner (applyContentZoom) updates the cache and persists — calling
      // registry.setZoom directly from main would move the view but skip
      // both.
      view.webContents.on('before-input-event', (event, input) => {
        if (input.type !== 'keyDown') return;
        if (input.key === 'F11') {
          this.toggleFullScreen();
          event.preventDefault();
          return;
        }
        const key = zoomChordKey(input);
        if (key) {
          this.cb.onZoomKey?.({ paneId, key });
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
      const namePill = new WebContentsView({
        webPreferences: { nodeIntegration: true, contextIsolation: false, sandbox: false },
      });
      const startHidden = this._globalHidden;
      e = { view, control, namePill, nameLabel, tileId, objectId, bounds: rounded, hidden: startHidden, focused: true, userZoom: contentZoom, lastUserClickMs: 0 };
      this.entries.set(paneId, e);
      this.win.contentView.addChildView(view);
      view.setBounds(startHidden ? parkedBounds(rounded.width, rounded.height) : rounded);
      this.win.contentView.addChildView(control);
      control.setBackgroundColor('#00000000');
      this.applyControlBounds(e);
      void control.webContents.loadURL(CONTROL_HTML);
      this.win.contentView.addChildView(namePill);
      namePill.setBackgroundColor('#00000000');
      this.applyNamePillBounds(e);
      // Re-push the owned label once the pill page is ready — the renderer's
      // first setNameLabel usually races this load.
      namePill.webContents.on('did-finish-load', () => this.pushNameLabel(e!));
      void namePill.webContents.loadURL(NAME_HTML);
      this.wireNav(paneId, e);
      this.applyMinWidthZoom(e);
      // A persisted back-stack revives with its history (issue #113); absent
      // or invalid falls back to a plain load — a corrupt blob must never
      // break revive.
      const h = parseHistory(history);
      if (h) {
        void view.webContents.navigationHistory.restore({ entries: h.entries, index: h.index });
      } else {
        void view.webContents.loadURL(url);
      }
      return;
    }

    e.userZoom = contentZoom; // the persisted zoom rides every (re)place
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
        this.applyNamePillBounds(e);
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
      this.applyNamePillBounds(e);
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

  // applyNamePillBounds places the native bubble at the view's top-center, or
  // parks it with everything else. Same one-source-of-truth rule as
  // applyControlBounds.
  private applyNamePillBounds(e: Entry): void {
    if (controlVisible(e.hidden, e.focused)) {
      e.namePill.setBounds(roundBounds(namePillBounds(e.bounds, NAME_PILL_W, NAME_PILL_H, NAME_PILL_MARGIN)));
    } else {
      e.namePill.setBounds(parkedBounds(NAME_PILL_W, NAME_PILL_H));
    }
  }

  // setNameLabel stores and pushes the bubble's label (issue #118). The
  // entry owns the text; did-finish-load re-pushes it, so ordering between
  // the renderer's push and the pill page's load cannot lose it.
  setNameLabel(paneId: string, label: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    e.nameLabel = label;
    this.pushNameLabel(e);
  }

  private pushNameLabel(e: Entry): void {
    void e.namePill.webContents
      .executeJavaScript(`window.setLabel(${JSON.stringify(e.nameLabel)})`)
      .catch(() => {}); // still loading — did-finish-load pushes again
  }

  // namePillPaneFor resolves a bubble view's webContents id back to its pane.
  namePillPaneFor(webContentsId: number): string | undefined {
    for (const [paneId, e] of this.entries) {
      if (e.namePill.webContents.id === webContentsId) return paneId;
    }
    return undefined;
  }

  // clearSiteData wipes the current SITE from the view's partition (issue
  // #136): every cookie of the same registrable domain — including SIBLING
  // subdomains, so clearing from mail.google.com reaches the login state on
  // accounts.google.com (issue #177) — plus storage (localStorage, IndexedDB,
  // service workers, caches) for the page origin and every matched cookie
  // host, then reloads so the site sees the cleared state. The next ascent
  // dehydrates the cleaned partition into the plugin DB, so the reset
  // persists.
  async clearSiteData(wc: WebContents): Promise<void> {
    let u: URL;
    try {
      u = new URL(wc.getURL());
    } catch {
      return;
    }
    const ses = wc.session;
    const cookies = await ses.cookies.get({});
    const matched = cookies.filter((c) => cookieDomainMatches(u.hostname, c.domain ?? ''));
    await Promise.all(
      matched.map((c) => {
        const proto = c.secure ? 'https' : 'http';
        const host = (c.domain ?? '').replace(/^\./, '');
        return ses.cookies.remove(`${proto}://${host}${c.path ?? '/'}`, c.name).catch(() => {});
      }),
    );
    const origins = storageOriginsFor(
      u.origin,
      matched.map((c) => c.domain ?? ''),
    );
    await Promise.all(
      origins.map((origin) => ses.clearStorageData({ origin }).catch(() => {})),
    );
    wc.reload();
  }

  // noteUserClick stamps the entry whose view the given webContents belongs
  // to: its preload just forwarded a left press — the one legitimate path to
  // OS focus for a live view (issue #172). The focus guard honors the stamp.
  noteUserClick(sender: WebContents): void {
    for (const e of this.entries.values()) {
      if (e.view.webContents === sender) {
        e.lastUserClickMs = Date.now();
        return;
      }
    }
  }

  // clearSiteDataFor is the pane-addressed variant (the e2e drives it —
  // Playwright cannot click a native menu).
  async clearSiteDataFor(paneId: string): Promise<void> {
    const e = this.entries.get(paneId);
    if (e) await this.clearSiteData(e.view.webContents);
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
    const z = composeZoom(minWidthZoomFactor(e.bounds.width, URL_MIN_LAYOUT_WIDTH), e.userZoom);
    try {
      e.view.webContents.setZoomFactor(z);
    } catch {
      // webContents not ready yet — wireNav re-applies on did-finish-load.
    }
  }

  // setZoom updates the USER content zoom for the pane's live view (the
  // tile's content_zoom, issue #82) and re-applies the composed factor.
  setZoom(paneId: string, zoom: number): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    e.userZoom = zoom;
    this.applyMinWidthZoom(e);
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
    this.applyNamePillBounds(e);
  }

  // remove captures a final frame + the page's URL/title, detaches and
  // destroys the view, and returns the freeze payload for persistence.
  async remove(paneId: string): Promise<FreezeResult> {
    const e = this.entries.get(paneId);
    if (!e) return { jpegBase64: '', url: '', title: '', history: '' };
    this.entries.delete(paneId);

    // Commit DOM storage (localStorage) to the persistent partition BEFORE
    // the renderer is closed. Chromium writes cookies eagerly but flushes
    // localStorage lazily, so an abrupt webContents.close() can drop recent
    // localStorage writes — which is exactly where GitLab autosaves an
    // unsubmitted comment draft. Flushing here is what makes that draft
    // survive ascend → descend → go-live. (The session itself is host-local
    // now — Chromium's own disk persistence is the system of record; there
    // is no dehydrate.)
    try {
      session.fromPartition(SESSION_PARTITION).flushStorageData();
    } catch {
      // Best-effort: the durable partition flushes on quit regardless.
    }

    let jpegBase64 = '';
    let url = '';
    let title = '';
    let history = '';
    try {
      url = e.view.webContents.getURL();
      title = e.view.webContents.getTitle();
      // The navigation back-stack, persisted so a revived tile can still go
      // "back" (issue #113). pageState is stripped (urls+titles only).
      const nav = e.view.webContents.navigationHistory;
      history = serializeHistory(nav.getAllEntries(), nav.getActiveIndex());
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
        this.win.contentView.removeChildView(e.namePill);
        e.namePill.webContents.close();
      } catch {
        // ignore
      }
    }
    return { jpegBase64, url, title, history };
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
    // A page-initiated navigation (self-refresh timer, meta-refresh, JS
    // reload) makes Chromium focus the new document's widget — silently
    // stealing OS keyboard focus from whatever the user was typing in
    // (issue #172), and the grab can land (and re-land) asynchronously after
    // any single navigation event, so the guard sits on the focus event
    // itself: a view may hold OS focus only when its pane is the focused
    // pane OR the user just pressed into it (the forwarded left-down stamps
    // lastUserClickMs before wasm marks the pane focused). Anything else is
    // a steal — hand focus back.
    const bounceStolenFocus = () => {
      if (e.focused) return;
      if (Date.now() - e.lastUserClickMs < USER_CLICK_FOCUS_GRACE_MS) return;
      this.cb.onFocusStolen?.({ paneId });
      // The grab can still be IN FLIGHT when the bounce runs — Chromium's
      // widget-focus commit then lands after it with no further focus
      // event. Recheck once the dust settles and bounce again if the view
      // still holds focus it shouldn't.
      setTimeout(() => {
        if (!e.focused && e.view.webContents.isFocused() &&
            Date.now() - e.lastUserClickMs >= USER_CLICK_FOCUS_GRACE_MS) {
          this.cb.onFocusStolen?.({ paneId });
        }
      }, FOCUS_RECHECK_MS);
    };
    e.view.webContents.on('focus', bounceStolenFocus);
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
