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
  restoreRefusedMessage,
  URL_MIN_LAYOUT_WIDTH,
  shouldSurfaceFailLoad,
  failLoadMessage,
  renderProcessGoneMessage,
  zoomChordKey,
  openBelowUrl,
} from './viewutil';
import { urlContextMenuTemplate } from './contextmenu';
import { captureAttempt, captureJpegBase64, describeAttempt } from './capture';
import { decideStreak, FRESH, StreakState } from './capturestreak';
import { decideFocus, isPressInput, GuardPhase } from './focusguard';

// __dirname is dist/main at runtime, so the compiled preload sits one level up.
const urlViewPreload = path.join(__dirname, '..', 'preload', 'urlview-preload.js');

interface Entry {
  view: WebContentsView;
  tileId: string;
  bounds: Bounds;
  hidden: boolean;
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
}

interface RegistryCallbacks {
  onNav?: (ev: NavEvent) => void;
  // index.ts wires this to sendError; the registry knows nothing of IPC.
  onError?: (ev: ErrorEvent) => void;
  onOpenBelow?: (ev: OpenBelowEvent) => void;
  onFreezeURL?: (ev: FreezeURLEvent) => void;
  // Both doors into the menu announce through here.
  onContextMenu?: (ev: ContextMenuEvent) => void;
  onZoomKey?: (ev: ZoomKeyEvent) => void;
  // focusguard.ts owns the verdict that a focus grab was a steal.
  onFocusStolen?: (ev: { paneId: string }) => void;
}

// WebviewRegistry owns the live url-tile WebContentsViews parented to the root
// window, one per paneId, all on SESSION_PARTITION. It knows nothing of IPC or
// the store: register.ts wires the Electron handlers to these methods.
export class WebviewRegistry {
  private readonly win: BaseWindow;
  private readonly cb: RegistryCallbacks;
  private readonly entries = new Map<string, Entry>();
  // The e2e reads this through __gwRegistry to tell a synthetic sendInputEvent
  // lost in the input pipeline from a real relay bug.
  zoomChordRelays = 0;

  constructor(win: BaseWindow, cb: RegistryCallbacks = {}) {
    this.win = win;
    this.cb = cb;
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
  async place(paneId: string, tileId: string, url: string, bounds: Bounds, contentZoom = 0, history = '', durable = false, hidden = false, focused = false): Promise<void> {
    const rounded = roundBounds(bounds);
    const partition = SESSION_PARTITION;
    const stale = this.entries.get(paneId);
    if (stale) {
      // closeURLStream is the one path that persists a freeze. Reaching here
      // means a view was replaced without it, so this freeze has no caller.
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
        // Stacked levels keep running while parked, so a call keeps ringing.
        backgroundThrottling: false,
        preload: urlViewPreload,
      },
    });
    // Nothing Chromium would open as a window or tab spawns a BrowserWindow.
    view.webContents.setWindowOpenHandler(({ url: target }) => {
      const below = openBelowUrl(target);
      if (below) {
        this.cb.onOpenBelow?.({ paneId, url: below });
      }
      return { action: 'deny' };
    });
    // window.ts handles F11 on the canvas, but a focused live view owns OS
    // keyboard focus, so that handler never sees the key. The zoom chord is
    // relayed, not applied, because setZoom from main would skip the
    // renderer's cache and its write.
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
    // The preload suppresses this for a right-drag, so this is a real click.
    view.webContents.on('context-menu', (_event, params) => this.showContextMenu(paneId, view, params));
    // hidden and focused start from the renderer's verdict for this frame,
    // because it owns both. hidden keeps a view placed under an open palette
    // off the canvas overlay; focused feeds the steal guard from the first
    // frame, because addChildView and loadURL hand the new widget OS focus even
    // on an unfocused pane.
    const startHidden = hidden;
    const e: Entry = { view, tileId, bounds: rounded, hidden: startHidden, focused, userZoom: contentZoom, presses: 0, durable, focusSettle: null, captureStreak: FRESH };
    this.entries.set(paneId, e);
    this.win.contentView.addChildView(view);
    view.setBounds(startHidden ? parkedBounds(rounded.width, rounded.height) : rounded);
    this.wireNav(paneId, e);
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
          this.cb.onError?.({ source: 'electron:webview', message: restoreRefusedMessage(paneId, err) });
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
      const cb = this.win.getContentBounds();
      e.view.webContents.sendInputEvent({
        type: 'mouseWheel',
        x: p.sx - cb.x - e.bounds.x,
        y: p.sy - cb.y - e.bounds.y,
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
      this.cb.onError?.({
        source: 'electron:webview',
        message: 'view crashed while closing — preview not updated',
      });
    } finally {
      // Runs even when the capture threw: the renderer has dropped this pane,
      // so a view left attached sits blank over what it ascended into.
      try {
        this.win.contentView.removeChildView(e.view);
        e.view.webContents.close();
      } catch (err) {
        this.cb.onError?.({
          source: 'electron:webview',
          message: 'failed to detach live view — ascend may leave a blank overlay: ' + String(err),
        });
      }
    }
    return { jpegBase64, url, title, history };
  }

  // A frame for mirroring, or '' when there is no live view or the attempt
  // failed. A hidden pane is not an attempt at all.
  async capture(paneId: string): Promise<string> {
    const e = this.entries.get(paneId);
    if (!e || e.hidden) return '';
    const attempt = await captureAttempt(e.view);
    const decision = decideStreak(e.captureStreak, attempt.kind);
    e.captureStreak = decision.state;
    const report = decision.report;
    if (report) {
      // A recovery is reported too, or the failing report reads as permanent.
      const message =
        report.kind === 'failing'
          ? `pane ${paneId}: mirror capture failing: ${describeAttempt(attempt)}`
          : `pane ${paneId}: mirror capture recovered after ${report.afterFailures} failed ` +
            `${report.afterFailures === 1 ? 'capture' : 'captures'}`;
      this.cb.onError?.({ source: 'electron:webview', message });
    }
    return attempt.kind === 'ok' ? attempt.jpegBase64 : '';
  }

  // The one back action: the bar's button and the menu's Back.
  goBack(paneId: string): void {
    const e = this.entries.get(paneId);
    if (!e) return;
    const nav = e.view.webContents.navigationHistory;
    if (nav.canGoBack()) nav.goBack();
  }

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
      if (act.kind === 'bounce') this.cb.onFocusStolen?.({ paneId });
      if (act.settleMs === null) return;
      if (e.focusSettle) clearTimeout(e.focusSettle);
      const bounced = act.kind === 'bounce';
      e.focusSettle = setTimeout(() => {
        e.focusSettle = null;
        if (this.entries.get(paneId) !== e) return; // removed meanwhile
        step('settle', pressesAtFocus, bounced);
      }, act.settleMs);
    };
    e.view.webContents.on('focus', () => step('focus-event', e.presses, false));
    // zoomFactor resets across cross-origin navigations.
    e.view.webContents.on('did-finish-load', () => this.applyMinWidthZoom(e));

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

    // Unreported, a crashed renderer just sits blank. getURL() after a crash
    // may throw, which must not stop the notice.
    e.view.webContents.on('render-process-gone', (_event, details) => {
      let url = '';
      try {
        url = e.view.webContents.getURL();
      } catch {
      }
      this.cb.onError?.({
        source: 'electron:webview',
        message: renderProcessGoneMessage(url, details.reason),
      });
    });
  }
}

