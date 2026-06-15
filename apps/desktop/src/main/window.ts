import { BrowserWindow, Menu, screen } from 'electron';
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

  // Size the initial bounds to the primary display's work area so a fresh
  // launch never opens larger than the screen (the old fixed 1600x1000 could
  // overflow a smaller display). We then maximize, but a sane initial size is
  // the fallback if the WM ignores the maximize request.
  const { width, height } = screen.getPrimaryDisplay().workAreaSize;

  const win = new BrowserWindow({
    width,
    height,
    backgroundColor: '#0c0d11',
    title: 'Gridwell',
    autoHideMenuBar: true,
    minimizable: true,
    maximizable: true,
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      // sandbox:false lets the preload require its sibling ipc module for the
      // channel constants. Safe here: single-tenant, loopback-only, first-party.
      sandbox: false,
    },
  });

  // Start maximized to fill the screen (within the WM's decorations) rather
  // than as a free-floating window.
  win.maximize();

  void win.loadURL(origin + '/');

  // When the display geometry changes (rotation, resolution / aspect change)
  // a fullscreen window can keep its old bounds and leave the canvas stretched
  // or letterboxed. Re-fit it to the (new) bounds of the display it's on so the
  // renderer gets a resize event and the canvas re-lays-out. Only while
  // fullscreen — a normal window is the user's to size.
  screen.on('display-metrics-changed', () => {
    if (win.isDestroyed() || !win.isFullScreen()) return;
    const d = screen.getDisplayMatching(win.getBounds());
    win.setBounds(d.bounds);
  });

  // The default menu (which we removed) used to provide the F11 fullscreen
  // accelerator; re-add it directly, plus a minimize accelerator. Active when
  // the canvas has focus — a focused URL tile's native view handles its own
  // keys (the live-URL view mirrors F11 separately in webviews.ts).
  //
  // Minimize (Ctrl/Cmd+M) is provided explicitly because the host window
  // manager may not surface a minimize control in its decorations (the
  // reported "I can only maximize / fullscreen, not minimize"); this gives a
  // guaranteed way down regardless of the WM.
  win.webContents.on('before-input-event', (event, input) => {
    if (input.type !== 'keyDown') return;
    if (input.key === 'F11') {
      win.setFullScreen(!win.isFullScreen());
      event.preventDefault();
    } else if ((input.control || input.meta) && (input.key === 'm' || input.key === 'M')) {
      win.minimize();
      event.preventDefault();
    }
  });

  return { win };
}
