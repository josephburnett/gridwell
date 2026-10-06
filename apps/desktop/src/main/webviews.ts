import { BaseWindow, WebContentsView, Menu, clipboard, session, WebContents } from 'electron';
import type { MenuItemConstructorOptions } from 'electron';
import * as path from 'node:path';
import type { WebContentsViewConstructorOptions } from 'electron';
import type { Bounds, FreezeResult, FrameEvent, NavEvent, ErrorEvent, NoticeSeverity, OpenBelowEvent, FreezeURLEvent, ContextMenuEvent, ZoomKeyEvent, EscapeKeyEvent, ViewGoneEvent } from './ipc';
import {
  SESSION_PARTITION,
  roundBounds,
  boundsEqual,
  parkedBounds,
  minWidthZoomFactor,
  composeZoom,
  serializeHistory,
  reviveNavigation,
  restoreRefused,
  restoreRefusedMessage,
  URL_MIN_LAYOUT_WIDTH,
  shouldSurfaceFailLoad,
  failLoadMessage,
  renderProcessGoneMessage,
  pageClosedMessage,
  zoomChordKey,
  openBelowUrl,
  toContentPoint,
} from './viewutil';
import { urlContextMenuTemplate } from './contextmenu';
import { capturable, captureAttempt, captureJpegBase64, describeAttempt } from './capture';
import { decideStreak, streakNotice, FRESH, StreakState } from './capturestreak';
import { decideFocus, isPressInput, GuardPhase } from './focusguard';
import { trace } from './trace';
import {
  menuChose,
  menuOpened,
  viewBounds,
  viewCreated,
  viewDestroyed,
  viewFailed,
  viewFocused,
  viewGone,
  viewMoved,
  viewNav,
  viewShown,
} from './viewtrace';

// The live view's page menu, as a trace record names it; register.ts names the
// other one.
const URL_MENU = 'url';

// __dirname is dist/main at runtime, so the compiled preload sits one level up.
const urlViewPreload = path.join(__dirname, '..', 'preload', 'urlview-preload.js');

interface Entry {
  view: WebContentsView;
  // The pane the view serves now. move() re-keys the entry, so every handler
  // reads the pane here, never the one the view was placed on.
  paneId: string;
  tileId: string;
  // The address main placed the view on, or last saw it reach. A dead
  // renderer answers getURL() with nothing, so a notice reads this.
  url: string;
  // The renderer's name for this view (client/urlview.Gen), echoed on its
  // gone event.
  gen: number;
  bounds: Bounds;
  hidden: boolean;
  // Whether the main frame is between did-start-navigation and its load, which
  // capture() reads through capture.capturable.
  navigating: boolean;
  focused: boolean;
  // userZoom is the tile's persisted content zoom; 0 means 1.0.
  userZoom: number;
  // Main sees a press before the renderer does, so no page can delay or
  // suppress the count.
  presses: number;
  durable: boolean;
  // Tracked so remove() can cancel it before the closure reads a closed view.
  focusSettle: ReturnType<typeof setTimeout> | null;
  captureStreak: StreakState;
  // Bumped by every show, hide and move, so a park still waiting on its frame
  // can tell it was overtaken.
  parkGen: number;
}

// How a view ended outside remove(). close is whether its webContents is still
// open, as a crashed renderer's is.
interface Ending {
  message: (url: string) => string;
  severity: NoticeSeverity;
  close: boolean;
}

interface RegistryCallbacks {
  onNav?: (ev: NavEvent) => void;
  // A mirror frame for the tile's preview cache; see mirror().
  onFrame?: (ev: FrameEvent) => void;
  // index.ts wires this to sendError; the registry knows nothing of IPC.
  onError?: (ev: ErrorEvent) => void;
  onOpenBelow?: (ev: OpenBelowEvent) => void;
  onFreezeURL?: (ev: FreezeURLEvent) => void;
  // Both doors into the menu announce through here.
  onContextMenu?: (ev: ContextMenuEvent) => void;
  onZoomKey?: (ev: ZoomKeyEvent) => void;
  onEscapeKey?: (ev: EscapeKeyEvent) => void;
  // focusguard.ts owns the verdict that a focus grab was a steal.
  onFocusStolen?: (ev: { paneId: string }) => void;
  // A view ended outside remove(); see retire().
  onViewGone?: (ev: ViewGoneEvent) => void;
}

// How a view is made, injected so webviews.test.ts runs the registry under
// node with a fake.
type NewView = (opts: WebContentsViewConstructorOptions) => WebContentsView;

// WebviewRegistry owns the live url-tile WebContentsViews parented to the root
// window, one per paneId, all on SESSION_PARTITION. It knows nothing of IPC or
// the store: register.ts wires the Electron handlers to these methods.
export class WebviewRegistry {
  private readonly win: BaseWindow;
  private readonly cb: RegistryCallbacks;
  private readonly entries = new Map<string, Entry>();
  // The e2e reads these through __gwRegistry to tell a synthetic
  // sendInputEvent lost in the input pipeline from a real relay bug.
  zoomChordRelays = 0;
  escapeRelays = 0;
  // The e2e reads this to see that an unread face costs no capture.
  mirrorCalls = 0;

  private readonly newView: NewView;

  constructor(win: BaseWindow, cb: RegistryCallbacks = {}, newView: NewView = (o) => new WebContentsView(o)) {
    this.win = win;
    this.cb = cb;
    this.newView = newView;
  }

  // Every notice the registry raises carries this one source, which
  // client/errsurface groups by. Everything here is a failure but the mirror's
  // recovery, so the severity defaults and only that caller passes one.
  private reportErr(message: string, severity: NoticeSeverity = 'error'): void {
    this.cb.onError?.({ source: 'electron:webview', message, severity });
  }

  // The canvas's own F11 handler cannot see the key while a view has focus.
  private toggleFullScreen(): void {
    this.win.setFullScreen(!this.win.isFullScreen());
  }

  // contextmenu.ts owns which items appear; this binds them to the clipboard
  // and webContents.
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
    // Before the pop, so this pane is focused by the time any item runs, and a
    // dismissed menu has still moved focus, as a left-click does.
    this.cb.onContextMenu?.({ paneId });
    trace(menuOpened(URL_MENU, paneId));
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
        // Only a durable tile can hold the freeze intent.
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
        chose: (label) => trace(menuChose(URL_MENU, label, paneId)),
        failed: (message) => this.reportErr(`pane ${paneId}: ${message}`),
      },
    );
    const menu = Menu.buildFromTemplate(template as MenuItemConstructorOptions[]);
    menu.popup({ window: this.win });
  }

  // The bar circle's door onto the same menu, with no in-page context. A page
  // can hijack contextmenu; this path always reaches it.
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

  tileIdFor(paneId: string): string | undefined {
    return this.entries.get(paneId)?.tileId;
  }

  focusedFor(paneId: string): boolean | undefined {
    return this.entries.get(paneId)?.focused;
  }

  // Test-only: a harness drives real Chromium focus and input with it.
  webContentsFor(paneId: string): WebContents | undefined {
    return this.entries.get(paneId)?.view.webContents;
  }

  // Test-only: the bounds Electron last set, which say whether it is parked.
  viewBoundsFor(paneId: string): { x: number; y: number; width: number; height: number } | undefined {
    const e = this.entries.get(paneId);
    if (!e) return undefined;
    return (e.view as unknown as { getBounds(): { x: number; y: number; width: number; height: number } }).getBounds();
  }

  // The view is a child of the window's contentView, so it paints above the
  // root canvas renderer. Later bounds arrive through setBounds.
  async place(paneId: string, tileId: string, url: string, bounds: Bounds, contentZoom = 0, history = '', durable = false, hidden = false, focused = false, gen = 0): Promise<void> {
    const rounded = roundBounds(bounds);
    const partition = SESSION_PARTITION;
    const stale = this.entries.get(paneId);
    if (stale) {
      // closeURLStream is the one path that persists a freeze. Reaching here
      // means a view was replaced without it, so this freeze has no caller.
      this.reportErr(`pane ${paneId}: live view replaced (${stale.tileId} → ${tileId}) without a close; its final frame is lost`);
      // remove() reports its own failures and the lost frame is already on
      // the strip, so a rejection here must not stop the replacement view.
      await this.remove(paneId).catch(() => {});
    }
    const view = this.newView({
      webPreferences: {
        partition,
        contextIsolation: true,
        nodeIntegration: false,
        // Stacked levels keep running while parked, so a call keeps ringing.
        backgroundThrottling: false,
        preload: urlViewPreload,
      },
    });
    // hidden and focused start from the renderer's verdict for this frame,
    // because it owns both. hidden keeps a view placed under an open palette
    // off the canvas overlay; focused feeds the steal guard from the first
    // frame, because addChildView and loadURL hand the new widget OS focus even
    // on an unfocused pane.
    const e: Entry = { view, paneId, tileId, url, gen, bounds: rounded, hidden, navigating: false, focused, userZoom: contentZoom, presses: 0, durable, focusSettle: null, captureStreak: FRESH, parkGen: 0 };
    // Nothing Chromium would open as a window or tab spawns a BrowserWindow.
    view.webContents.setWindowOpenHandler(({ url: target }) => {
      const below = openBelowUrl(target);
      if (below) {
        this.cb.onOpenBelow?.({ paneId: e.paneId, url: below });
      }
      return { action: 'deny' };
    });
    // window.ts handles F11 on the canvas, but a focused live view owns OS
    // keyboard focus, so that handler never sees the key. The zoom chord is
    // relayed, not applied, because setZoom from main would skip the
    // renderer's cache and its write. A parked view is off screen, so the Esc
    // it hears is the canvas's: a press the view forwarded keeps OS focus here
    // through the drag it became, and that drag owns Esc.
    view.webContents.on('before-input-event', (event, input) => {
      if (input.type !== 'keyDown') return;
      if (input.key === 'F11') {
        this.toggleFullScreen();
        event.preventDefault();
        return;
      }
      if (input.key === 'Escape' && e.hidden) {
        this.escapeRelays++;
        this.cb.onEscapeKey?.({ paneId: e.paneId });
        event.preventDefault();
        return;
      }
      const key = zoomChordKey(input);
      if (key) {
        this.zoomChordRelays++;
        this.cb.onZoomKey?.({ paneId: e.paneId, key });
        event.preventDefault();
      }
    });
    // The preload suppresses this for a right-drag, so this is a real click.
    view.webContents.on('context-menu', (_event, params) => this.showContextMenu(e.paneId, view, params));
    this.entries.set(paneId, e);
    trace(viewCreated(paneId, tileId, url));
    this.win.contentView.addChildView(view);
    view.setBounds(hidden ? parkedBounds(rounded.width, rounded.height) : rounded);
    this.wireNav(e);
    this.applyMinWidthZoom(e);
    // reviveNavigation owns the tie-break between the persisted back-stack and
    // the tile's user-editable address.
    const nav = reviveNavigation(url, history);
    if (nav.kind === 'restore') {
      // A url Chromium refuses outright commits nothing and fires no
      // did-fail-load, so the pane sits blank and this report is the only
      // notice. Loading the address after it would only repeat the refusal:
      // reviveNavigation restores a stack only when its active entry is that
      // same address.
      view.webContents.navigationHistory
        .restore({ entries: nav.history.entries, index: nav.history.index })
        .catch((err: unknown) => {
          if (restoreRefused(this.entries.get(e.paneId) === e, err)) {
            this.reportErr(restoreRefusedMessage(e.paneId, err));
          }
        });
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
    trace(viewBounds(paneId, e.tileId, rounded));
    if (!e.hidden) {
      e.view.setBounds(rounded);
    }
    this.applyMinWidthZoom(e);
  }

  // One step of a single-finger drag becomes a mouseWheel, because Chromium
  // does not gesture-scroll raw touches inside an embedded WebContentsView (see
  // urlview-preload.ts). The content follows the finger; the harness pins it.
  touchScroll(sender: WebContents, p: { sx: number; sy: number; dx: number; dy: number }): void {
    for (const e of this.entries.values()) {
      if (e.view.webContents !== sender) continue;
      const c = toContentPoint(this.win, p);
      e.view.webContents.sendInputEvent({
        type: 'mouseWheel',
        x: c.x - e.bounds.x,
        y: c.y - e.bounds.y,
        deltaX: p.dx,
        deltaY: p.dy,
        // Precise deltas, so the page tracks the finger 1:1 instead of running
        // the wheel's animated smoothing.
        hasPreciseScrollingDeltas: true,
      });
      return;
    }
  }

  // Keeps a narrow pane from reflowing to a cramped mobile layout;
  // minWidthZoomFactor owns the arithmetic.
  private applyMinWidthZoom(e: Entry): void {
    const z = composeZoom(minWidthZoomFactor(e.bounds.width, URL_MIN_LAYOUT_WIDTH), e.userZoom);
    try {
      e.view.webContents.setZoomFactor(z);
    } catch {
      // webContents not ready yet; wireNav re-applies on did-finish-load.
    }
  }

  setZoom(paneId: string, zoom: number): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    e.userZoom = zoom;
    this.applyMinWidthZoom(e);
  }

  // Parking rather than destroying is what lets a canvas overlay paint where
  // the native view sits. syncURLViews calls this every frame.
  // A park is the one moment the pane reads its own face, and a parked view
  // has nothing to capture, so the view stays up until the face is taken.
  setHidden(paneId: string, hidden: boolean, focused: boolean): void {
    const e = this.entries.get(paneId);
    if (!e || (e.hidden === hidden && e.focused === focused)) return;
    const viewChanged = e.hidden !== hidden;
    // Before the flip, because capture reads hidden before its first await.
    const face = viewChanged && hidden ? this.mirror(paneId) : null;
    e.hidden = hidden;
    e.focused = focused;
    if (!viewChanged) return;
    trace(viewShown(paneId, e.tileId, hidden));
    const gen = ++e.parkGen;
    if (!face) {
      e.view.setBounds(e.bounds);
      return;
    }
    void face.then(() => {
      if (this.entries.get(e.paneId) === e && e.parkGen === gen) {
        e.view.setBounds(parkedBounds(e.bounds.width, e.bounds.height));
      }
    });
  }

  // move hands the view on fromPaneId to toPaneId with its page: the entry is
  // re-keyed and takes the new pane's bounds and verdicts, and nothing
  // navigates. It throws when fromPaneId holds no view, which the renderer
  // hears as a refusal and drops the handle it moved. Absent bounds keep the
  // view's own, for a pane not laid out yet.
  move(fromPaneId: string, toPaneId: string, bounds: Bounds | undefined, durable: boolean, hidden: boolean, focused: boolean): void {
    const e = this.entries.get(fromPaneId);
    if (!e) throw new Error(`pane ${fromPaneId}: no live view to move`);
    const stale = this.entries.get(toPaneId);
    if (stale && stale !== e) {
      // As in place(): the renderer closes a pane's own view before a move
      // lands on it, so reaching here loses that view's final frame.
      this.reportErr(`pane ${toPaneId}: live view replaced (${stale.tileId} → ${e.tileId}) without a close; its final frame is lost`);
      void this.remove(toPaneId).catch(() => {});
    }
    this.entries.delete(fromPaneId);
    e.paneId = toPaneId;
    this.entries.set(toPaneId, e);
    // The settle and a park waiting on its face were armed for the old pane.
    if (e.focusSettle) {
      clearTimeout(e.focusSettle);
      e.focusSettle = null;
    }
    e.parkGen++;
    if (bounds) e.bounds = roundBounds(bounds);
    e.hidden = hidden;
    e.focused = focused;
    e.durable = durable;
    e.view.setBounds(hidden ? parkedBounds(e.bounds.width, e.bounds.height) : e.bounds);
    this.applyMinWidthZoom(e);
    trace(viewMoved(fromPaneId, toPaneId, e.tileId));
    // A view that keeps OS focus in a pane that lost it would take the next
    // keystrokes, the steal the guard exists for.
    try {
      if (!focused && e.view.webContents.isFocused()) this.cb.onFocusStolen?.({ paneId: toPaneId });
    } catch {
      // A view gone under the move retires on its own event.
    }
  }

  async remove(paneId: string): Promise<FreezeResult> {
    const e = this.entries.get(paneId);
    if (!e) return { jpegBase64: '', url: '', title: '', history: '' };
    this.entries.delete(paneId);
    // The settle closure holds this view, and firing after close() would throw
    // uncaught in main.
    if (e.focusSettle) {
      clearTimeout(e.focusSettle);
      e.focusSettle = null;
    }

    // Chromium flushes localStorage lazily, so an abrupt close() can drop a
    // site's unsubmitted comment draft.
    try {
      session.fromPartition(SESSION_PARTITION).flushStorageData();
    } catch {
      // The durable partition flushes on quit regardless.
    }

    let jpegBase64 = '';
    let url = '';
    let title = '';
    let history = '';
    try {
      url = e.view.webContents.getURL();
      title = e.view.webContents.getTitle();
      const nav = e.view.webContents.navigationHistory;
      history = serializeHistory(nav.getAllEntries(), nav.getActiveIndex());
      jpegBase64 = await captureJpegBase64(e.view);
    } catch {
      // bridgeRemove in client/wasm/url_stream_client.go drops an empty freeze
      // rather than writing back, so a good preview survives. The crash still
      // surfaces, so the user knows why the tile went stale.
      this.reportErr('view crashed while closing — preview not updated');
    } finally {
      // Runs even when the capture threw: the renderer has dropped this pane,
      // so a view left attached sits blank over what it ascended into.
      try {
        this.win.contentView.removeChildView(e.view);
        e.view.webContents.close();
      } catch (err) {
        this.reportErr('failed to detach live view — ascend may leave a blank overlay: ' + String(err));
      }
    }
    trace(viewDestroyed(paneId, e.tileId, url));
    return { jpegBase64, url, title, history };
  }

  // A frame for mirroring, or '' when there is no live view or the attempt
  // failed. capturable owns which panes are an attempt at all.
  async capture(paneId: string): Promise<string> {
    const e = this.entries.get(paneId);
    if (!e || !capturable(e)) return '';
    const attempt = await captureAttempt(e.view);
    // A view that ended while the frame was taken has already said so.
    if (this.entries.get(e.paneId) !== e) return '';
    const decision = decideStreak(e.captureStreak, attempt.kind);
    e.captureStreak = decision.state;
    const report = decision.report;
    if (report) {
      // A recovery is reported too, or the failing report reads as permanent.
      const notice = streakNotice(paneId, report, describeAttempt(attempt));
      this.reportErr(notice.message, notice.severity);
    }
    return attempt.kind === 'ok' ? attempt.jpegBase64 : '';
  }

  // mirror captures one frame into the tile's preview cache, where every pane
  // showing the tile reads it. It settles with no frame rather than rejecting.
  async mirror(paneId: string): Promise<void> {
    this.mirrorCalls++;
    const jpegBase64 = await this.capture(paneId);
    const tileId = this.entries.get(paneId)?.tileId;
    if (jpegBase64 && tileId !== undefined) this.cb.onFrame?.({ paneId, tileId, jpegBase64 });
  }

  // The one back action: the bar's button and the menu's Back.
  goBack(paneId: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    const nav = e.view.webContents.navigationHistory;
    if (nav.canGoBack()) nav.goBack();
  }

  // reload loads url in the pane's view again, the navigation place makes,
  // for a page its source changed (urlview.PageMoved).
  reload(paneId: string, url: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    void e.view.webContents.loadURL(url);
  }

  async removeAll(): Promise<void> {
    await Promise.all(this.paneIds().map((id) => this.remove(id)));
  }

  private wireNav(e: Entry): void {
    const emit = () => {
      const url = e.view.webContents.getURL();
      if (url) e.url = url;
      this.cb.onNav?.({ paneId: e.paneId, tileId: e.tileId, url, title: e.view.webContents.getTitle() });
    };
    e.view.webContents.on('did-navigate', emit);
    e.view.webContents.on('did-navigate-in-page', emit);
    e.view.webContents.on('page-title-updated', emit);
    // isPressInput excludes the registry's own mouseWheel injection.
    e.view.webContents.on('input-event', (_event, input) => {
      if (isPressInput(input.type)) e.presses++;
    });
    // A page-initiated navigation makes Chromium focus the new document's
    // widget, and the grab can re-land asynchronously after any one navigation
    // event, so the guard sits on the focus event and asks focusguard.
    //
    // A view can also die outside remove(), and a read of a destroyed
    // WebContents throws uncaught inside a timer, hanging main behind an error
    // dialog, so the settle reads isFocused() inside a catch.
    const step = (phase: GuardPhase, pressesAtFocus: number, alreadyBounced: boolean): void => {
      let viewHoldsOSFocus = true; // at 'focus-event' the event is the evidence
      if (phase === 'settle') {
        try {
          viewHoldsOSFocus = e.view.webContents.isFocused();
        } catch {
          return; // the view died between the grab and this settle
        }
      }
      const act = decideFocus({
        phase,
        paneFocused: e.focused,
        viewHoldsOSFocus,
        pressesAtFocus,
        pressesNow: e.presses,
        alreadyBounced,
      });
      if (act.kind === 'allow') return;
      if (act.kind === 'bounce') this.cb.onFocusStolen?.({ paneId: e.paneId });
      if (act.settleMs === null) return;
      if (e.focusSettle) clearTimeout(e.focusSettle);
      const bounced = act.kind === 'bounce';
      e.focusSettle = setTimeout(() => {
        e.focusSettle = null;
        if (this.entries.get(e.paneId) !== e) return; // removed or moved meanwhile
        step('settle', pressesAtFocus, bounced);
      }, act.settleMs);
    };
    e.view.webContents.on('focus', () => {
      trace(viewFocused(e.paneId, e.tileId, true));
      step('focus-event', e.presses, false);
    });
    e.view.webContents.on('blur', () => trace(viewFocused(e.paneId, e.tileId, false)));
    // Both halves of a navigation, because the gap between them is where a
    // page hangs with the pane sitting blank.
    // A same-document navigation keeps the surface and fires no load, so
    // counting one would strand the flag and stop the mirror for good.
    e.view.webContents.on('did-start-navigation', (details) => {
      if (!details.isMainFrame || details.isSameDocument) return;
      e.navigating = true;
      trace(viewNav(e.paneId, e.tileId, false, details.url));
    });
    // zoomFactor resets across cross-origin navigations.
    e.view.webContents.on('did-finish-load', () => {
      e.navigating = false;
      // The view can die between the load and this callback, and a read of a
      // destroyed WebContents throws uncaught in main, which hangs it behind
      // an error dialog.
      try {
        trace(viewNav(e.paneId, e.tileId, true, e.view.webContents.getURL()));
      } catch {
        return;
      }
      this.applyMinWidthZoom(e);
    });

    e.view.webContents.on(
      'did-fail-load',
      (_event, errorCode, errorDescription, validatedURL, isMainFrame) => {
        if (!isMainFrame) return;
        // Every way a navigation ends clears the flag, an abort included, or
        // the pane's mirror would never be read again.
        e.navigating = false;
        const message = failLoadMessage(validatedURL, errorDescription, errorCode);
        // Traced even when it is not surfaced: an aborted navigation is noise
        // on the strip and evidence in a dump.
        trace(viewFailed(e.paneId, e.tileId, message));
        if (!shouldSurfaceFailLoad(errorCode, isMainFrame)) return;
        this.reportErr(message);
      },
    );

    // A dead renderer leaves a blank view whose only Reload is a context menu
    // it can no longer raise, so the view is gone and the next descent places
    // a new one.
    e.view.webContents.on('render-process-gone', (_event, details) =>
      this.retire(e, { message: (url) => renderProcessGoneMessage(url, details.reason), severity: 'error', close: true }),
    );
    // A page that calls window.close() destroys its webContents with no word
    // to the renderer.
    e.view.webContents.on('destroyed', () => this.retire(e, { message: pageClosedMessage, severity: 'info', close: false }));
  }

  // retire is the one owner of "the view is gone" for a webContents that ended
  // outside remove(): the entry goes at once, so every later call for the pane
  // is a no-op and remove() answers an empty freeze, and the renderer is told
  // so the pane shows the tile's frozen face instead of a blank view.
  private retire(e: Entry, end: Ending): void {
    const paneId = e.paneId;
    // remove() takes the entry before it closes the view.
    if (this.entries.get(paneId) !== e) return;
    this.entries.delete(paneId);
    if (e.focusSettle) {
      clearTimeout(e.focusSettle);
      e.focusSettle = null;
    }
    const url = e.url;
    try {
      this.win.contentView.removeChildView(e.view);
    } catch {
      // A view whose webContents is gone may already be detached.
    }
    if (end.close) {
      try {
        e.view.webContents.close();
      } catch (err) {
        this.reportErr(`pane ${paneId}: failed to close a dead view: ${String(err)}`);
      }
    }
    trace(viewGone(paneId, e.tileId, url));
    this.cb.onViewGone?.({ paneId, tileId: e.tileId, gen: e.gen });
    this.reportErr(end.message(url), end.severity);
  }
}

