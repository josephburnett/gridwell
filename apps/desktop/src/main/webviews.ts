import { BaseWindow, WebContentsView, session } from 'electron';
import * as path from 'node:path';
import type { Bounds, FreezeResult, NavEvent } from './ipc';
import { SESSION_PARTITION, partitionFor, roundBounds, boundsEqual } from './viewutil';
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

// controlBounds positions the corner button at the bottom-right of a view's
// content box.
function controlBounds(b: Bounds): Bounds {
  return {
    x: b.x + b.width - CONTROL_SIZE - CONTROL_MARGIN,
    y: b.y + b.height - CONTROL_SIZE - CONTROL_MARGIN,
    width: CONTROL_SIZE,
    height: CONTROL_SIZE,
  };
}

const OFFSCREEN = { x: -100000, y: -100000 };

export interface RegistryCallbacks {
  // onNav fires when a hosted view finishes a navigation (URL/title change),
  // so the renderer can update the cached tile address.
  onNav?: (ev: NavEvent) => void;
}

// WebviewRegistry owns the live URL-tile WebContentsViews parented to the
// root window. One view per paneId; every view shares the single persistent
// SESSION_PARTITION, so all tiles act like tabs of one browser (shared
// cookies/logins and DOM storage). The registry is deliberately free of IPC
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

  has(paneId: string): boolean {
    return this.entries.has(paneId);
  }

  paneIds(): string[] {
    return [...this.entries.keys()];
  }

  // tileIdFor returns the tile id hosted in paneId, or undefined.
  tileIdFor(paneId: string): number | undefined {
    return this.entries.get(paneId)?.tileId;
  }

  // place creates (or re-targets) the view for paneId. If a view already
  // exists for the pane it's reused; a URL change re-navigates it. The view
  // is added as a child of the window's contentView, so it paints above the
  // root canvas renderer at the given bounds.
  async place(paneId: string, tileId: number, objectId: string, url: string, bounds: Bounds, pluginUuid: string): Promise<void> {
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
    // for it loads, so url tiles open already logged in. Once per partition.
    if (pluginUuid && !this.hydrated.has(partition)) {
      this.hydrated.add(partition);
      await hydratePartition(this.origin, pluginUuid);
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
      e = { view, control, tileId, objectId, bounds: rounded, hidden: false, partition, pluginUuid };
      this.entries.set(paneId, e);
      this.win.contentView.addChildView(view);
      view.setBounds(rounded);
      this.win.contentView.addChildView(control);
      control.setBackgroundColor('#00000000');
      control.setBounds(roundBounds(controlBounds(rounded)));
      void control.webContents.loadURL(CONTROL_HTML);
      this.wireNav(paneId, e);
      this.applyMinWidthZoom(e);
      void view.webContents.loadURL(url);
      return;
    }

    // Reuse: update bounds and, if the URL changed, navigate.
    e.tileId = tileId;
    if (!boundsEqual(e.bounds, rounded)) {
      e.bounds = rounded;
      e.view.setBounds(rounded);
      e.control.setBounds(roundBounds(controlBounds(rounded)));
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
      e.control.setBounds(roundBounds(controlBounds(rounded)));
    }
    this.applyMinWidthZoom(e);
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
    const w = e.bounds.width;
    const z = w >= URL_MIN_LAYOUT_WIDTH ? 1 : Math.max(0.25, w / URL_MIN_LAYOUT_WIDTH);
    try {
      e.view.webContents.setZoomFactor(z);
    } catch {
      // webContents not ready yet — wireNav re-applies on did-finish-load.
    }
  }

  // setHidden hides/shows the view without destroying it. Used during drag
  // gestures and modals so canvas-drawn overlays (palette, ghosts) can paint
  // where the native view would otherwise sit on top. Hiding parks the view
  // off-screen (a portable stand-in for visibility toggling).
  setHidden(paneId: string, hidden: boolean): void {
    const e = this.entries.get(paneId);
    if (!e || e.hidden === hidden) return;
    e.hidden = hidden;
    if (hidden) {
      e.view.setBounds({ ...OFFSCREEN, width: e.bounds.width, height: e.bounds.height });
      e.control.setBounds({ ...OFFSCREEN, width: CONTROL_SIZE, height: CONTROL_SIZE });
    } else {
      e.view.setBounds(e.bounds);
      e.control.setBounds(roundBounds(controlBounds(e.bounds)));
    }
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
      void dehydratePartition(this.origin, e.pluginUuid);
      jpegBase64 = await captureJpegBase64(e.view);
    } catch {
      // Best-effort: a crashed/destroyed view yields an empty freeze.
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
  }
}

// URL_MIN_LAYOUT_WIDTH is the narrowest layout width a live URL view renders
// at; below this the page is zoomed to fit rather than reflowed. See
// applyMinWidthZoom.
const URL_MIN_LAYOUT_WIDTH = 640;

// clearSharedSession wipes the shared cookies/storage for ALL tiles. Not
// used yet (a future "sign out" gesture); since the session is shared, this
// logs every tile out at once — there is no per-tile session to clear.
export async function clearSharedSession(): Promise<void> {
  await session.fromPartition(SESSION_PARTITION).clearStorageData();
}
