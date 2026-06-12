import { BrowserWindow, Menu } from 'electron';
import * as path from 'node:path';

export interface RootWindow {
  win: BrowserWindow;
}

// createRootWindow builds the top-level window that hosts the Gridwell
// renderer (the WASM canvas app served by the sidecar).
//
// It's a BrowserWindow, not a bare BaseWindow + manually-sized
// WebContentsView: a BrowserWindow's web contents auto-fills the window, so
// the canvas always matches the window size. The manual-layout approach was
// fragile under tiling / HiDPI window managers (e.g. WSLg), which could leave
// the canvas stuck at a stale size with bare window background showing.
//
// Live URL tiles are still WebContentsView children added to win.contentView
// (BrowserWindow inherits contentView from BaseWindow); they paint on top of
// the canvas. The WebviewRegistry takes this window unchanged.
export function createRootWindow(origin: string): RootWindow {
  // Gridwell is a mouse-only canvas; the default Electron application menu
  // (File/Edit/View/…) only eats space and, on Linux, renders inside the
  // window. Remove it entirely.
  Menu.setApplicationMenu(null);

  const win = new BrowserWindow({
    width: 1600,
    height: 1000,
    backgroundColor: '#0c0d11',
    title: 'Gridwell',
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      // sandbox:false lets the preload require its sibling ipc module for the
      // channel constants. Safe here: single-tenant, loopback-only, first-party.
      sandbox: false,
    },
  });

  void win.loadURL(origin + '/');

  // The default menu (which we removed) used to provide the F11 fullscreen
  // accelerator; re-add it directly. Active when the canvas has focus — a
  // focused URL tile's native view handles its own keys.
  win.webContents.on('before-input-event', (event, input) => {
    if (input.type === 'keyDown' && input.key === 'F11') {
      win.setFullScreen(!win.isFullScreen());
      event.preventDefault();
    }
  });

  return { win };
}
