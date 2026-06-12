import { BaseWindow, WebContentsView, session } from 'electron';
import type { Bounds, FreezeResult, NavEvent } from './ipc';
import { partitionFor, roundBounds, boundsEqual } from './viewutil';
import { captureJpegBase64 } from './capture';

interface Entry {
  view: WebContentsView;
  tileId: number;
  objectId: string;
  bounds: Bounds;
  hidden: boolean;
}

export interface RegistryCallbacks {
  // onNav fires when a hosted view finishes a navigation (URL/title change),
  // so the renderer can update the cached tile address.
  onNav?: (ev: NavEvent) => void;
}

// WebviewRegistry owns the live URL-tile WebContentsViews parented to the
// root window. One view per paneId; the session partition is keyed by the
// tile's objectId. The registry is deliberately free of IPC and store
// knowledge — ipc.ts wires Electron handlers to these methods, and the
// renderer remains the only thing that talks to the Go backend.
export class WebviewRegistry {
  private readonly win: BaseWindow;
  private readonly cb: RegistryCallbacks;
  private readonly entries = new Map<string, Entry>();

  constructor(win: BaseWindow, cb: RegistryCallbacks = {}) {
    this.win = win;
    this.cb = cb;
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
  place(paneId: string, tileId: number, objectId: string, url: string, bounds: Bounds): void {
    const rounded = roundBounds(bounds);
    let e = this.entries.get(paneId);

    if (e && e.objectId !== objectId) {
      // Different tile in the same pane — tear the old view down first so
      // we don't leak a view or cross sessions.
      this.remove(paneId).catch(() => {});
      e = undefined;
    }

    if (!e) {
      const view = new WebContentsView({
        webPreferences: {
          partition: partitionFor(objectId),
          contextIsolation: true,
          nodeIntegration: false,
        },
      });
      e = { view, tileId, objectId, bounds: rounded, hidden: false };
      this.entries.set(paneId, e);
      this.win.contentView.addChildView(view);
      view.setBounds(rounded);
      this.wireNav(paneId, e);
      void view.webContents.loadURL(url);
      return;
    }

    // Reuse: update bounds and, if the URL changed, navigate.
    e.tileId = tileId;
    if (!boundsEqual(e.bounds, rounded)) {
      e.bounds = rounded;
      e.view.setBounds(rounded);
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
    if (!e.hidden) e.view.setBounds(rounded);
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
      e.view.setBounds({ x: -100000, y: -100000, width: e.bounds.width, height: e.bounds.height });
    } else {
      e.view.setBounds(e.bounds);
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
      jpegBase64 = await captureJpegBase64(e.view);
    } catch {
      // Best-effort: a crashed/destroyed view yields an empty freeze.
    }
    try {
      this.win.contentView.removeChildView(e.view);
      // Free the underlying renderer process.
      e.view.webContents.close();
    } catch {
      // ignore
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
  }
}

// clearPersistedSession wipes a tile's stored cookies/storage. Not used yet
// (a future "log out this tile" gesture); exposed so the partition naming
// has a single owner.
export async function clearPersistedSession(objectId: string): Promise<void> {
  const part = partitionFor(objectId);
  await session.fromPartition(part).clearStorageData();
}
