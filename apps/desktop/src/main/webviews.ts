import { BaseWindow, WebContentsView, Menu, clipboard, session, WebContents } from 'electron';
import type { MenuItemConstructorOptions } from 'electron';
import * as path from 'node:path';
import type { Bounds, FreezeResult, NavEvent, ErrorEvent, OpenBelowEvent, FreezeURLEvent, ContextMenuEvent, ZoomKeyEvent } from './ipc';
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

// urlViewPreload is the script injected into every live url view; it forwards
// a right-button press to main so the renderer can gesture over live content.
// __dirname is dist/main at runtime, so the compiled preload sits one level up.
const urlViewPreload = path.join(__dirname, '..', 'preload', 'urlview-preload.js');

interface Entry {
  view: WebContentsView;
  tileId: string;
  bounds: Bounds;
  hidden: boolean;
  // focused is whether this pane is the focused pane, as the renderer last
  // reported it through setHidden. The focus-steal guard reads it.
  focused: boolean;
  // userZoom is the tile's persisted content zoom, composed with the min-width
  // layout zoom in applyMinWidthZoom. 0 means unset, i.e. 1.0.
  userZoom: number;
  // lastUserClickMs is when the view's preload last forwarded a left press, the
  // one legitimate way a view acquires OS focus. The focus guard treats a grab
  // inside this grace window as user intent.
  lastUserClickMs: number;
  // durable is whether the tile behind this view survives ascent. An ephemeral
  // visit is not durable and has nothing to re-descend into, so the context
  // menu offers no Freeze Page there.
  durable: boolean;
  // focusRecheck is the steal guard's pending settle timer. It is tracked so
  // remove() can cancel it: the closure holds the view, and firing after
  // webContents.close() would throw uncaught in main.
  focusRecheck: ReturnType<typeof setTimeout> | null;
  // captureFailing marks a mirror capture in a failing streak, so entering and
  // leaving failure each log exactly once. A silently frozen mirror otherwise
  // leaves no evidence anywhere.
  captureFailing?: boolean;
}

// USER_CLICK_FOCUS_GRACE_MS is how long after a forwarded left press a view may
// legitimately acquire OS focus. Native focus lands immediately on the press,
// while the wasm marks the pane focused a round trip later, and the stamp
// bridges that gap. It is long enough for a slow frame and far shorter than any
// refresh cadence worth stealing for.
const USER_CLICK_FOCUS_GRACE_MS = 1500;

// FOCUS_RECHECK_MS is the settle delay before the steal guard double-checks:
// long enough for an in-flight widget-focus commit to land, short enough
// that leaked keystrokes stay negligible.
const FOCUS_RECHECK_MS = 120;

interface RegistryCallbacks {
  // onNav fires when a hosted view finishes a navigation, changing url or
  // title, so the renderer can update the cached tile address.
  onNav?: (ev: NavEvent) => void;
  // onError fires for every webview failure the registry detects:
  // did-fail-load, render-process-gone, a crash during remove(). index.ts wires
  // it to sendError(rootWC, ...), the one path onto EV.error. The registry
  // knows nothing of IPC; it only reports, and index.ts decides how the report
  // reaches the renderer.
  onError?: (ev: ErrorEvent) => void;
  // onOpenBelow fires when a hosted view's page tries to open a new window
  // through target=_blank, window.open, or a ctrl/cmd-click. The renderer
  // splits the pane and opens the url as an ephemeral visit below.
  onOpenBelow?: (ev: OpenBelowEvent) => void;
  // onFreezeURL fires when the user picks "Freeze Page" in a live view's
  // context menu; the renderer freezes and stores the intent.
  onFreezeURL?: (ev: FreezeURLEvent) => void;
  // onContextMenu fires just before a live view's context menu opens, naming
  // the pane it acts in. The renderer moves focus there: a right-click is an
  // interaction with that pane, and the rule is the same one a left-click
  // obeys. Announced once here, for both doors into the menu, because this is
  // the only place that knows a menu is opening at all.
  onContextMenu?: (ev: ContextMenuEvent) => void;
  // onZoomKey fires when the content-zoom chord (Ctrl/Cmd with +, =, - or 0) is
  // pressed while this view owns OS keyboard focus. The renderer's
  // applyContentZoom, the one owner of the cache and the write, handles it.
  onZoomKey?: (ev: ZoomKeyEvent) => void;
  // onFocusStolen fires when a live view acquired OS keyboard focus without the
  // user acting on its pane: a page-initiated navigation makes Chromium focus
  // the new document's widget. index.ts hands focus back to the root window's
  // webContents, where the canvas and every shell overlay live.
  onFocusStolen?: (ev: { paneId: string }) => void;
}

// WebviewRegistry owns the live url-tile WebContentsViews parented to the root
// window. One view per paneId, and every view browses on the one host-local
// persistent partition (SESSION_PARTITION). The registry knows nothing of IPC
// or the store: ipc.ts wires Electron handlers to these methods, and the
// renderer stays the only thing that talks to the Go backend.
export class WebviewRegistry {
  private readonly win: BaseWindow;
  private readonly cb: RegistryCallbacks;
  private readonly entries = new Map<string, Entry>();
  // Count of zoom chords seen by before-input-event and relayed to the
  // renderer. The e2e reads it through __gwRegistry as a delivery ack: a
  // synthetic sendInputEvent that never bumps this was lost in the input
  // pipeline, which is an xvfb artifact rather than a product path, while a bump
  // with no zoom effect is a real relay bug. It only grows, and only the e2e
  // reads it.
  zoomChordRelays = 0;

  constructor(win: BaseWindow, cb: RegistryCallbacks = {}) {
    this.win = win;
    this.cb = cb;
  }

  // toggleFullScreen flips the host window's fullscreen state. The F11 handler
  // injected into live url views calls it, because the canvas's own F11 handler
  // cannot see the key while a native view has focus.
  private toggleFullScreen(): void {
    this.win.setFullScreen(!this.win.isFullScreen());
  }

  // showContextMenu builds and pops the live url view's right-click menu. Which
  // items appear and what each does lives in the pure, unit-tested
  // urlContextMenuTemplate; this only translates Electron's params, binds the
  // actions to the real clipboard and webContents, and pops the menu over the
  // window. params is the subset of ContextMenuParams the template reads, which
  // ContextMenuParams satisfies structurally. showMenu supplies an empty one,
  // since the bar-circle path has no in-page context.
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
    // Focus first, then pop: the menu acts in this pane, so this pane is the
    // focused one by the time any item runs. The renderer's focusToPane is the
    // one owner; announcing before the pop means even a menu dismissed without
    // a pick has moved focus, exactly like a bare left-click.
    this.cb.onContextMenu?.({ paneId });
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
        // Only a durable tile can hold the freeze intent; an ephemeral visit
        // has nothing to re-descend into.
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

  // showMenu pops the same context menu a right-click inside the view shows,
  // with no in-page context: no link, no selection, not editable. This is the
  // bar circle's right-click door. A page can hijack contextmenu and make the
  // in-page path unreachable, but the circle sits on the canvas outside the
  // view's rect, so this path always reaches Freeze Page.
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

  // focusedFor reports whether the registry believes paneId is the focused
  // pane — the fact the focus-steal guard reads, owned by the renderer and
  // carried on both place and setHidden. Undefined if the pane has no entry.
  focusedFor(paneId: string): boolean | undefined {
    return this.entries.get(paneId)?.focused;
  }

  // viewBoundsFor is a test-only accessor returning the view's physical bounds
  // as Electron last set them, which tells whether the view is parked or at its
  // visible position. The e2e uses it to assert that a bounds change while
  // hidden does not lift the view out of its parked position. Returns undefined
  // if the pane has no entry.
  viewBoundsFor(paneId: string): { x: number; y: number; width: number; height: number } | undefined {
    const e = this.entries.get(paneId);
    if (!e) return undefined;
    // View inherits getBounds() from Electron's View base class.
    return (e.view as unknown as { getBounds(): { x: number; y: number; width: number; height: number } }).getBounds();
  }

  // place creates the view for paneId. The view is a child of the window's
  // contentView, so it paints above the root canvas renderer at the given
  // bounds. Later bounds changes arrive through setBounds, every frame from
  // syncURLViews. A place() for a pane that already holds a view is a renderer
  // bug and is reported, never absorbed: url_stream_client.go returns early for
  // the tile already live in the pane and closes any other view first, so
  // nothing legitimate reaches that branch.
  async place(paneId: string, tileId: string, url: string, bounds: Bounds, contentZoom = 0, history = '', durable = false, hidden = false, focused = false): Promise<void> {
    const rounded = roundBounds(bounds);
    // One host-local session: every live url tile, local or through a mount,
    // browses on the shared persistent partition, so a login holds everywhere.
    const partition = SESSION_PARTITION;
    const stale = this.entries.get(paneId);
    if (stale) {
      // The renderer closes a pane's live view first (placeURLView calls
      // closeURLStream, the one path that persists a freeze) and never
      // re-places the tile already live there. Reaching here means a view was
      // replaced without its close, so surface it and then tear the old view
      // down so nothing leaks. The freeze remove() returns has no caller to
      // land in, which is why this must be loud.
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
        // Stacked levels keep their views running while parked off-screen, so
        // a hidden call keeps ringing. Chromium would otherwise throttle an
        // occluded page's timers.
        backgroundThrottling: false,
        // Forwards a right-button press to main → renderer so pane gestures
        // work over live content. Safe on arbitrary pages: it only listens
        // for button 2 and uses ipcRenderer, nothing else.
        preload: urlViewPreload,
      },
    });
    // target=_blank, window.open, ctrl-click: everything Chromium would open as
    // a new window or tab arrives here. Never spawn a detached BrowserWindow.
    // The url goes to the renderer, which splits the pane and opens it as an
    // ephemeral visit in the lower half: real Chromium on the tile's persistent
    // session, in a pane beside the page it came from, and gone on ascent.
    // openBelowUrl filters to web urls only, so a non-web protocol opens
    // nowhere, matching the session's openExternal deny.
    view.webContents.setWindowOpenHandler(({ url: target }) => {
      const below = openBelowUrl(target);
      if (below) {
        this.cb.onOpenBelow?.({ paneId, url: below });
      }
      return { action: 'deny' };
    });
    // F11 fullscreen: window.ts handles F11 on the canvas, but a focused live
    // url view owns OS keyboard focus, so that handler never sees the key.
    // Mirroring it here toggles fullscreen whichever view is focused.
    // The content-zoom chord is intercepted the same way and relayed to the
    // renderer, where applyContentZoom updates the cache and persists. Calling
    // registry.setZoom from main would move the view and skip both.
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
    // A plain right-click over live content must show a context menu: copy
    // link, copy, back, and the rest. A WebContentsView has no default menu; it
    // only emits this event. The injected preload suppresses the event for a
    // right-drag, which is a pane gesture, so reaching here means a real click.
    view.webContents.on('context-menu', (_event, params) => this.showContextMenu(paneId, view, params));
    // hidden and focused both start from the renderer's verdict for this frame
    // (PlaceArgs), because the renderer owns both facts. hidden parks a view
    // placed while the palette is open or during a drag gesture instead of
    // landing it on top of the canvas overlay. focused feeds the steal guard
    // below from the very first frame: addChildView and loadURL hand the new
    // widget OS keyboard focus, and a placement on an unfocused pane — a
    // workspace restore walking every leaf, an ascent re-engaging every
    // content pane, a promote — must bounce it straight back. Guessing `true`
    // here made the guard return early and leak the user's keystrokes into a
    // page they never clicked on. syncURLViews calls setHidden for this pane on
    // the next draw() and reaffirms both.
    const startHidden = hidden;
    const e: Entry = { view, tileId, bounds: rounded, hidden: startHidden, focused, userZoom: contentZoom, lastUserClickMs: 0, durable, focusRecheck: null };
    this.entries.set(paneId, e);
    this.win.contentView.addChildView(view);
    view.setBounds(startHidden ? parkedBounds(rounded.width, rounded.height) : rounded);
    this.wireNav(paneId, e);
    this.applyMinWidthZoom(e);
    // A persisted back-stack revives with its history. Absent, invalid, or
    // disagreeing with the tile's user-editable address, it falls back to a
    // plain load of the address; reviveNavigation owns that tie-break and is
    // unit-tested.
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

  // noteUserClick stamps the entry whose view the given webContents belongs to:
  // its preload just forwarded a left press, the one legitimate path to OS
  // focus for a live view. The focus guard honors the stamp.
  noteUserClick(sender: WebContents): void {
    for (const e of this.entries.values()) {
      if (e.view.webContents === sender) {
        e.lastUserClickMs = Date.now();
        return;
      }
    }
  }

  // touchScroll injects one step of a single-finger drag as a mouseWheel into
  // the view whose preload forwarded it; Chromium does not gesture-scroll raw
  // touches inside an embedded WebContentsView (see urlview-preload.ts). The
  // finger's screen position converts to view-local coords so the wheel lands on
  // the scrollable element under the finger. The content follows the finger, as
  // on any touch surface, which under sendInputEvent's wheel convention is the
  // finger's own delta; the capture harness's scroll assertion pins the sign.
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
        // Precise, touchpad-style deltas: the page tracks the finger 1:1
        // instead of running the wheel's animated smoothing.
        hasPreciseScrollingDeltas: true,
      });
      return;
    }
  }

  // applyMinWidthZoom keeps a narrow url pane from reflowing the page to a
  // cramped mobile layout: below URL_MIN_LAYOUT_WIDTH the page zooms out so it
  // still lays out at the min width and scales to fit. A native WebContentsView
  // cannot render wider than its bounds and be clipped to the pane, so this
  // scale-to-fit is the closest thing to a min width with horizontal scroll,
  // short of offscreen rendering. zoomFactor resets on cross-origin navigation,
  // so wireNav re-applies it on load.
  private applyMinWidthZoom(e: Entry): void {
    const z = composeZoom(minWidthZoomFactor(e.bounds.width, URL_MIN_LAYOUT_WIDTH), e.userZoom);
    try {
      e.view.webContents.setZoomFactor(z);
    } catch {
      // webContents not ready yet; wireNav re-applies on did-finish-load.
    }
  }

  // setZoom updates the user content zoom for the pane's live view (the tile's
  // content_zoom) and re-applies the composed factor.
  setZoom(paneId: string, zoom: number): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    e.userZoom = zoom;
    this.applyMinWidthZoom(e);
  }

  // setHidden shows or hides the view without destroying it, and tracks whether
  // the pane is focused. `hidden` parks the whole view off-screen during drag
  // gestures and modals, so canvas-drawn overlays such as the palette and drag
  // ghosts can paint where the native view would otherwise sit on top.
  // `focused` feeds the focus-steal guard: only the focused pane's view may keep
  // OS keyboard focus. syncURLViews calls this every frame, so it no-ops when
  // nothing changed.
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

  // remove captures a final frame plus the page's url and title, detaches and
  // destroys the view, and returns the freeze payload for persistence.
  async remove(paneId: string): Promise<FreezeResult> {
    const e = this.entries.get(paneId);
    if (!e) return { jpegBase64: '', url: '', title: '', history: '' };
    this.entries.delete(paneId);
    // Cancel the steal guard's settle timer: its closure holds this view, and
    // firing after close() would throw uncaught in main.
    if (e.focusRecheck) {
      clearTimeout(e.focusRecheck);
      e.focusRecheck = null;
    }

    // Commit DOM storage to the persistent partition before the renderer is
    // closed. Chromium writes cookies eagerly but flushes localStorage lazily,
    // so an abrupt webContents.close() can drop recent localStorage writes,
    // which is where a site keeps an unsubmitted comment draft. Flushing here
    // is what makes such a draft survive ascend, descend, and go-live.
    // Chromium's own disk persistence is the system of record for the session.
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
      // back. pageState is stripped, leaving urls and titles.
      const nav = e.view.webContents.navigationHistory;
      history = serializeHistory(nav.getAllEntries(), nav.getActiveIndex());
      jpegBase64 = await captureJpegBase64(e.view);
    } catch {
      // A crashed or destroyed view yields an empty freeze. That is safe: the
      // wasm-side guard (bridgeRemove in client/wasm/url_stream_client.go,
      // `if len(jpeg)>0 || url!="" || title!=""`) skips the writeback entirely
      // when all three come back empty, so an empty freeze cannot overwrite a
      // good preview with a blank one. What must surface is the crash itself,
      // so the user knows why the tile fell back to its last good preview
      // instead of the page simply disappearing.
      this.cb.onError?.({
        source: 'electron:webview',
        message: 'view crashed while closing — preview not updated',
      });
    } finally {
      // Detach and free the view whatever the capture did. This must run even
      // if the capture above threw or timed out: the renderer has already
      // dropped this pane from its live set, so a view left attached would sit
      // blank on top of the pane the user just ascended out of, while every
      // other pane shows the frozen preview fine.
      try {
        this.win.contentView.removeChildView(e.view);
        e.view.webContents.close();
      } catch (err) {
        // This is the state the comment above forbids: a live view left sitting
        // on top of the pane the user ascended out of. It must not fail
        // silently, so a blank rectangle covering a pane comes with its cause.
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
      // A frozen mirror must not be evidence-free. Log the transition into
      // failure once per streak; per-frame captures would spam.
      if (!e.captureFailing) {
        e.captureFailing = true;
        this.cb.onError?.({ source: 'electron:webview', message: `pane ${paneId}: mirror capture failing: ${String(err)}` });
      }
      return '';
    }
  }

  // goBack is the one back action for a live view: the bar's back button over
  // IPC and the context menu's Back both land here. It no-ops at the start of
  // the history.
  goBack(paneId: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    const nav = e.view.webContents.navigationHistory;
    if (nav.canGoBack()) nav.goBack();
  }

  // removeAll tears everything down, on app quit or window close.
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
    // A page-initiated navigation (a self-refresh timer, meta-refresh, or JS
    // reload) makes Chromium focus the new document's widget, taking OS
    // keyboard focus from whatever the user was typing in. The grab can land,
    // and re-land, asynchronously after any single navigation event, so the
    // guard sits on the focus event itself: a view may hold OS focus only when
    // its pane is the focused pane, or when the user just pressed into it (the
    // forwarded left-down stamps lastUserClickMs before wasm marks the pane
    // focused). Anything else is a steal, and focus goes back.
    const bounceStolenFocus = () => {
      if (e.focused) return;
      if (Date.now() - e.lastUserClickMs < USER_CLICK_FOCUS_GRACE_MS) return;
      this.cb.onFocusStolen?.({ paneId });
      // The grab can still be in flight when the bounce runs: Chromium's
      // widget-focus commit then lands after it with no further focus event.
      // Recheck once things settle and bounce again if the view still holds
      // focus it should not. The timer is tracked on the entry so remove() can
      // cancel it, but that only covers a teardown that went through the
      // registry: a view can also die under it — a render-process crash, a
      // host-side close — and every read of a destroyed WebContents throws,
      // uncaught inside a timer, which hangs main behind an error dialog. The
      // whole recheck therefore runs inside a catch; a view that is gone has no
      // focus to hand back.
      if (e.focusRecheck) clearTimeout(e.focusRecheck);
      e.focusRecheck = setTimeout(() => {
        e.focusRecheck = null;
        if (this.entries.get(paneId) !== e) return; // removed meanwhile
        try {
          if (!e.focused && e.view.webContents.isFocused() &&
              Date.now() - e.lastUserClickMs >= USER_CLICK_FOCUS_GRACE_MS) {
            this.cb.onFocusStolen?.({ paneId });
          }
        } catch {
          // The view died between the focus event and this settle.
        }
      }, FOCUS_RECHECK_MS);
    };
    e.view.webContents.on('focus', bounceStolenFocus);
    // zoomFactor resets across cross-origin navigations, so re-apply the
    // min-width zoom once the new document has loaded.
    e.view.webContents.on('did-finish-load', () => this.applyMinWidthZoom(e));

    // Unhandled, did-fail-load leaves a live url view blank with no signal to
    // the user. Chromium also fires it constantly for benign reasons, so
    // shouldSurfaceFailLoad filters out a cancelled or superseded navigation
    // and any subframe failure; only a genuine main-frame failure gets through.
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

    // render-process-gone means the renderer process crashed, from an OOM or a
    // GPU crash; unreported, the view just sits blank. getURL() after a crash
    // may throw, and that must not stop the notice.
    e.view.webContents.on('render-process-gone', (_event, details) => {
      let url = '';
      try {
        url = e.view.webContents.getURL();
      } catch {
        // renderProcessGoneMessage handles an empty url cleanly
      }
      this.cb.onError?.({
        source: 'electron:webview',
        message: renderProcessGoneMessage(url, details.reason),
      });
    });
  }
}

