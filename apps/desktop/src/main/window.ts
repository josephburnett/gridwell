import { BaseWindow, WebContentsView } from 'electron';
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
  const win = new BaseWindow({
    width: 1600,
    height: 1000,
    backgroundColor: '#0c0d11',
    title: 'Gridwell',
  });

  const root = new WebContentsView({
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
    },
  });

  win.contentView.addChildView(root);
  layoutRoot(win, root);
  win.on('resize', () => layoutRoot(win, root));

  void root.webContents.loadURL(origin + '/');
  return { win, root };
}

// layoutRoot keeps the root view filling the window's content area.
function layoutRoot(win: BaseWindow, root: WebContentsView): void {
  const [w, h] = win.getContentSize();
  root.setBounds({ x: 0, y: 0, width: w, height: h });
}
