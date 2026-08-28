import { BaseWindow, WebContentsView, Menu, clipboard, session, WebContents } from 'electron';
import type { MenuItemConstructorOptions } from 'electron';
import * as path from 'node:path';
import type { Bounds, FreezeResult, NavEvent, ErrorEvent, OpenBelowEvent, FreezeURLEvent, ZoomKeyEvent } from './ipc';
import {
  SESSION_PARTITION,
  roundBounds,
  boundsEqual,
  parkedBounds,
  minWidthZoomFactor,
  composeZoom,
  serializeHistory,
  reviveNavigation,
  URL_MIN_LAYOUT_WIDTH,
  shouldSurfaceFailLoad,
  failLoadMessage,
  renderProcessGoneMessage,
  zoomChordKey,
  openBelowUrl,
} from './viewutil';
import { urlContextMenuTemplate } from './contextmenu';
import { captureJpegBase64 } from './capture';

// urlViewPreload is the script injected into every live URL view; it forwards
// a right-button press to main so the renderer can gesture over live content.
// __dirname is dist/main at runtime, so the compiled preload sits one level up.
const urlViewPreload = path.join(__dirname, '..', 'preload', 'urlview-preload.js');

interface Entry {
  view: WebContentsView;
  tileId: string;
  objectId: string;
  bounds: Bounds;
  hidden: boolean;
  // focused is whether this pane is the focused pane, as the renderer last
  // reported it (setHidden) — kept for the focus-steal guard's bookkeeping.
  focused: boolean;
  // userZoom is the tile's persisted content zoom (issue #82); composed with
  // the min-width layout zoom in applyMinWidthZoom. 0 = unset (1.0).
  userZoom: number;
  // lastUserClickMs is when the view's preload last forwarded a left press —
  // the one legitimate way a view acquires OS focus (issue #172). The focus
  // guard treats a grab inside this grace window as user intent.
  lastUserClickMs: number;
  // durable is whether the tile behind this view survives ascent — false for
  // an ephemeral visit, which has nothing to re-descend into, so the context
  // menu offers no Freeze Page there (issue #240).
  durable: boolean;
  // focusRecheck is the steal guard's pending settle-timer (issue #172).
  // Tracked so remove() can cancel it: the closure holds the view, and
  // firing after webContents.close() would throw uncaught in main.
  focusRecheck: ReturnType<typeof setTimeout> | null;
  // captureFailing marks a mirror capture in a failing streak, so the
  // transition into (and out of) failure logs exactly once — a silently
  // frozen mirror otherwise leaves no evidence anywhere (charter §6).
  captureFailing?: boolean;
}

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

// The corner control views are GONE (issue #214): the circle button lives in
// the renderer's bottom bar, outside every view's rect. The pure park/zoom
// math stays in viewutil (parkedBounds, minWidthZoomFactor), unit-tested.

interface RegistryCallbacks {
  // onNav fires when a hosted view finishes a navigation (URL/title change),
  // so the renderer can update the cached tile address.
  onNav?: (ev: NavEvent) => void;
  // onError fires for every webview failure the registry detects —
  // did-fail-load, render-process-gone, a crash during remove().
  // index.ts wires this to sendError(rootWC, ...),
  // which is the ONE path onto EV.error (issue #46). The registry itself
  // stays free of IPC knowledge — it only reports; index.ts decides how the
  // report reaches the renderer.
  onError?: (ev: ErrorEvent) => void;
  // onOpenBelow fires when a hosted view's page tries to open a NEW WINDOW
  // (target=_blank, window.open, ctrl/cmd-click). The renderer splits the
  // pane and opens the url as an ephemeral visit below (issue #111).
  onOpenBelow?: (ev: OpenBelowEvent) => void;
  // onFreezeURL fires when the user picks "Freeze Page" in a live view's
  // context menu (issue #237); the renderer freezes and stores the intent.
  onFreezeURL?: (ev: FreezeURLEvent) => void;
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
// root window. One view per paneId; every view browses on the ONE
// host-local persistent partition (SESSION_PARTITION, owner decision
// 2026-07-26). The registry is deliberately free of IPC
// and store knowledge — ipc.ts wires Electron handlers to these methods, and
// the renderer remains the only thing that talks to the Go backend.
export class WebviewRegistry {
  private readonly win: BaseWindow;
  private readonly cb: RegistryCallbacks;
  private readonly entries = new Map<string, Entry>();
  // Count of zoom chords seen by before-input-event and relayed to the
  // renderer. Read by the e2e (via __gwRegistry) as a delivery ACK: a
  // synthetic sendInputEvent that never bumps this was lost in the input
  // pipeline (an xvfb reality, not a product path); a bump with no zoom
  // effect is a real relay bug. Grows monotonically; e2e-only readers.
  zoomChordRelays = 0;

  constructor(win: BaseWindow, cb: RegistryCallbacks = {}) {
    this.win = win;
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
  // params is the subset of Electron's ContextMenuParams the template reads
  // (ContextMenuParams satisfies it structurally); showMenu supplies an empty
  // one — the bar-circle path has no in-page context.
  private showContextMenu(
    paneId: string,
    view: WebContentsView,
    params: {
      linkURL: string;
      selectionText: string;
      isEditable: boolean;
      editFlags: { canCut: boolean; canCopy: boolean; canPaste: boolean };
    },
  ): void {
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
        // Only a DURABLE tile can hold the freeze intent — an ephemeral
        // visit has nothing to re-descend into (issue #240).
        canFreeze: this.entries.get(paneId)?.durable ?? false,
      },
      {
        copyText: (t) => clipboard.writeText(t),
        copyLink: (u) => clipboard.writeText(u),
        openLink: (u) => void wc.loadURL(u),
        cut: () => wc.cut(),
        paste: () => wc.paste(),
        back: () => this.goBack(paneId),
        forward: () => {
          if (nav.canGoForward()) nav.goForward();
        },
        reload: () => wc.reload(),
        freeze: () => this.cb.onFreezeURL?.({ paneId }),
      },
    );
    const menu = Menu.buildFromTemplate(template as MenuItemConstructorOptions[]);
    menu.popup({ window: this.win });
  }

  // showMenu pops the SAME context menu a right-click inside the view shows,
  // with no in-page context (no link, no selection, not editable). This is
  // the bar circle's right-click door: a page can hijack contextmenu and
  // make the in-page path unreachable, but the circle sits on the canvas,
  // outside the view's rect, so this path always reaches Freeze Page.
  showMenu(paneId: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    this.showContextMenu(paneId, e.view, {
      linkURL: '',
      selectionText: '',
      isEditable: false,
      editFlags: { canCut: false, canCopy: false, canPaste: false },
    });
  }

  has(paneId: string): boolean {
    return this.entries.has(paneId);
  }

  paneIds(): string[] {
    return [...this.entries.keys()];
  }

  // tileIdFor returns the tile id hosted in paneId, or undefined.
  tileIdFor(paneId: string): string | undefined {
    return this.entries.get(paneId)?.tileId;
  }

  // viewBoundsFor is a test-only accessor that returns the view's actual
  // physical bounds as Electron last set them, revealing whether the view is
  // currently parked or at its intended (visible) position. Used by e2e to
  // assert that a bounds change while hidden does NOT lift the view out of
  // its parked position. Returns undefined if the pane has no entry.
  viewBoundsFor(paneId: string): { x: number; y: number; width: number; height: number } | undefined {
    const e = this.entries.get(paneId);
    if (!e) return undefined;
    // View inherits getBounds() from Electron's View base class.
    return (e.view as unknown as { getBounds(): { x: number; y: number; width: number; height: number } }).getBounds();
  }

  // place creates the view for paneId. The view is added as a child of the
  // window's contentView, so it paints above the root canvas renderer at
  // the given bounds. Bounds changes after placement arrive through
  // setBounds (every frame, from syncURLViews). A place() for a pane that
  // already holds a view is a renderer bug and is reported, never absorbed:
  // the old reuse path was unreachable (url_stream_client.go returns early
  // for the tile already live in the pane and closes any other view first)
  // and half-implemented (it ignored durable/history/hidden and skipped the
  // min-width zoom).
  async place(paneId: string, tileId: string, objectId: string, url: string, bounds: Bounds, contentZoom = 0, history = '', durable = false, hidden = false): Promise<void> {
    const rounded = roundBounds(bounds);
    // ONE host-local session (owner decision 2026-07-26): every live url
    // tile, local or through a mount, browses on the shared persistent
    // partition — your own logins everywhere. The per-plugin partitions and
    // their hydrate/dehydrate choreography are gone.
    const partition = SESSION_PARTITION;
    const stale = this.entries.get(paneId);
    if (stale) {
      // The renderer closes a pane's live view FIRST (placeURLView →
      // closeURLStream — the one path that persists a freeze) and never
      // re-places the tile already live there, so reaching here means a
      // view was replaced without its close: surface it, then tear the old
      // view down so at least nothing leaks. The freeze that remove()
      // returns has no caller to land in — which is exactly why this must
      // be loud.
      this.cb.onError?.({
        source: 'electron:webview',
        message: `pane ${paneId}: live view replaced (${stale.tileId} → ${tileId}) without a close; its final frame is lost`,
      });
      await this.remove(paneId).catch(() => {});
    }
    const view = new WebContentsView({
      webPreferences: {
        partition,
        contextIsolation: true,
        nodeIntegration: false,
        // Stacked levels keep their views RUNNING while parked
        // off-screen (issue #249 — a hidden Zoom call keeps ringing);
        // Chromium would otherwise throttle an occluded page's timers.
        backgroundThrottling: false,
        // Forwards a right-button press to main → renderer so pane gestures
        // work over live content. Safe on arbitrary pages: it only listens
        // for button 2 and uses ipcRenderer, nothing else.
        preload: urlViewPreload,
      },
    });
    // target=_blank / window.open / ctrl-click: everything Chromium would
    // open as a new window or tab arrives here. Never spawn a detached
    // BrowserWindow — instead hand the url to the renderer, which splits
    // the pane and opens it as an EPHEMERAL visit in the lower half
    // (issue #111): the link opens in real Chromium on the tile's
    // persistent session (no popup bot-guard friction), in a pane you can
    // read next to the page you came from, and it dies on ascent.
    // openBelowUrl filters to web urls only (issue #232) — a non-web
    // protocol opens nowhere, matching the session's openExternal deny.
    view.webContents.setWindowOpenHandler(({ url: target }) => {
      const below = openBelowUrl(target);
      if (below) {
        this.cb.onOpenBelow?.({ paneId, url: below });
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
        this.zoomChordRelays++;
        this.cb.onZoomKey?.({ paneId, key });
        event.preventDefault();
      }
    });
    // A plain right-click over live content must show a context menu (copy
    // link, copy, back, …). Electron's WebContentsView has NO default menu —
    // it only emits this event and leaves the menu to us. The injected
    // preload already suppresses this event for a right-DRAG (a pane
    // gesture), so reaching here means a genuine click.
    view.webContents.on('context-menu', (_event, params) => this.showContextMenu(paneId, view, params));
    // focused starts true: a pane only goes live by an action on the focused
    // pane, so the control should appear immediately; syncURLViews corrects
    // it on the next frame if focus has already moved.
    // hidden starts from the renderer's verdict for THIS frame (PlaceArgs.hidden)
    // so a view placed while the palette is open (or during a drag gesture)
    // starts parked rather than landing on top of the canvas overlay.
    // (It used to come from the last setHidden seen — a value that ranged
    // over a Go map, so a parked stacked level could park a new view at
    // random.) syncURLViews will call setHidden for this pane on
    // the next draw() and reaffirm the correct state.
    const startHidden = hidden;
    const e: Entry = { view, tileId, objectId, bounds: rounded, hidden: startHidden, focused: true, userZoom: contentZoom, lastUserClickMs: 0, durable, focusRecheck: null };
    this.entries.set(paneId, e);
    this.win.contentView.addChildView(view);
    view.setBounds(startHidden ? parkedBounds(rounded.width, rounded.height) : rounded);
    this.wireNav(paneId, e);
    this.applyMinWidthZoom(e);
    // A persisted back-stack revives with its history (issue #113); absent,
    // invalid, or DISAGREEING with the tile's (user-editable) address falls
    // back to a plain load of the address — reviveNavigation owns the
    // tie-break and is unit-tested.
    const nav = reviveNavigation(url, history);
    if (nav.kind === 'restore') {
      void view.webContents.navigationHistory.restore({ entries: nav.history.entries, index: nav.history.index });
    } else {
      void view.webContents.loadURL(url);
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
    }
    this.applyMinWidthZoom(e);
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

  // touchScroll injects one step of a single-finger drag as a mouseWheel into
  // the view whose preload forwarded it (Chromium does not gesture-scroll raw
  // touches inside an embedded WebContentsView — see urlview-preload.ts). The
  // finger's screen position converts to view-local coords so the wheel lands
  // on the scrollable element under the finger. Delta sign: the content
  // follows the finger, like every touch surface — sendInputEvent's wheel
  // convention makes that the finger's own delta, pinned by the capture
  // harness's scroll assertion.
  touchScroll(sender: WebContents, p: { sx: number; sy: number; dx: number; dy: number }): void {
    for (const e of this.entries.values()) {
      if (e.view.webContents !== sender) continue;
      const cb = this.win.getContentBounds();
      e.view.webContents.sendInputEvent({
        type: 'mouseWheel',
        x: p.sx - cb.x - e.bounds.x,
        y: p.sy - cb.y - e.bounds.y,
        deltaX: p.dx,
        deltaY: p.dy,
        // Precise (touchpad-style) deltas: the page tracks the finger 1:1
        // instead of running the wheel's animated smoothing.
        hasPreciseScrollingDeltas: true,
      });
      return;
    }
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
  // where the native view would otherwise sit on top. `focused` feeds the
  // focus-steal guard: only the focused pane's view may keep OS keyboard
  // focus (issue #172). Called
  // every frame from syncURLViews, so it no-ops when nothing changed.
  setHidden(paneId: string, hidden: boolean, focused: boolean): void {
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
  }

  // remove captures a final frame + the page's URL/title, detaches and
  // destroys the view, and returns the freeze payload for persistence.
  async remove(paneId: string): Promise<FreezeResult> {
    const e = this.entries.get(paneId);
    if (!e) return { jpegBase64: '', url: '', title: '', history: '' };
    this.entries.delete(paneId);
    // Cancel the steal guard's settle timer (issue #172): its closure holds
    // this view, and firing after close() would throw uncaught in main.
    if (e.focusRecheck) {
      clearTimeout(e.focusRecheck);
      e.focusRecheck = null;
    }

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
      } catch (err) {
        // This is EXACTLY the state the comment above forbids — a live view
        // left sitting on top of the pane the user ascended out of. It must
        // not fail silently (charter §6): say so, so "a blank rectangle
        // covers my pane" comes with its cause attached.
        this.cb.onError?.({
          source: 'electron:webview',
          message: 'failed to detach live view — ascend may leave a blank overlay: ' + String(err),
        });
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
      const jpeg = await captureJpegBase64(e.view);
      if (e.captureFailing && jpeg) {
        e.captureFailing = false;
        this.cb.onError?.({ source: 'electron:webview', message: `pane ${paneId}: mirror capture recovered` });
      }
      return jpeg;
    } catch (err) {
      // A frozen mirror must not be evidence-free: log the transition
      // into failure once per streak (per-frame captures would spam).
      if (!e.captureFailing) {
        e.captureFailing = true;
        this.cb.onError?.({ source: 'electron:webview', message: `pane ${paneId}: mirror capture failing: ${String(err)}` });
      }
      return '';
    }
  }

  // goBack is THE back action for a live view — the bar's back button (IPC)
  // and the context menu's Back both land here. A no-op at the start of the
  // history.
  goBack(paneId: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    const nav = e.view.webContents.navigationHistory;
    if (nav.canGoBack()) nav.goBack();
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
      // still holds focus it shouldn't. The timer is tracked on the entry
      // and guarded on liveness: a middle-click ascent inside the settle
      // window destroys the view, and isFocused() on a destroyed
      // WebContents throws uncaught in main.
      if (e.focusRecheck) clearTimeout(e.focusRecheck);
      e.focusRecheck = setTimeout(() => {
        e.focusRecheck = null;
        if (this.entries.get(paneId) !== e) return; // removed meanwhile
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

