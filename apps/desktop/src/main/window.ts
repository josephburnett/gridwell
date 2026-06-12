import { BaseWindow, WebContentsView, Menu } from 'electron';
import * as path from 'node:path';

export interface RootWindow {
  win: BaseWindow;
  root: WebContentsView;
}

// createRootWindow builds the top-level BaseWindow and a single root
// WebContentsView that fills it and loads the Gridwell renderer (the
// WASM canvas app served by the sidecar). Future phases add more
// WebContentsViews as siblings for live URL tiles; starting on
// BaseWindow + WebContentsView now means that's a pure addition, not a
// rework.
export function createRootWindow(origin: string): RootWindow {
  // Gridwell is a mouse-only canvas; the default Electron application menu
  // (File/Edit/View/…) only eats space and, on Linux, renders inside the
  // window. Remove it entirely.
  Menu.setApplicationMenu(null);

  const win = new BaseWindow({
    width: 1600,
    height: 1000,
    backgroundColor: '#0c0d11',
    title: 'Gridwell',
    autoHideMenuBar: true,
  });

  const root = new WebContentsView({
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      // sandbox:false lets the preload require its sibling ipc module for
      // the channel constants. Safe here: single-tenant, loopback-only, and
      // the preload is first-party. (Phase 5 can bundle the preload to a
      // self-contained file and re-enable the sandbox.)
      sandbox: false,
    },
  });

  win.contentView.addChildView(root);
  layoutRoot(win, root);

  // The window manager (e.g. WSLg) may open or resize the window to a size
  // other than the one we requested, and the timing of when getContentSize()
  // reports the final size varies. Re-layout on every resize, and again
  // shortly after creation, so the root view always fills the real content
  // area instead of getting stuck at a stale size (which would leave the
  // canvas clipped with bare window background showing on the bottom/right).
  win.on('resize', () => layoutRoot(win, root));
  setTimeout(() => layoutRoot(win, root), 0);
  setTimeout(() => layoutRoot(win, root), 200);

  void root.webContents.loadURL(origin + '/');
  return { win, root };
}

// layoutRoot keeps the root view filling the window's content area.
function layoutRoot(win: BaseWindow, root: WebContentsView): void {
  if (win.isDestroyed()) return;
  const [w, h] = win.getContentSize();
  if (w <= 0 || h <= 0) return;
  console.log(`[window] layout root view to ${w}x${h}`);
  root.setBounds({ x: 0, y: 0, width: w, height: h });
}
