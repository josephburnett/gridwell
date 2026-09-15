import { BrowserWindow, Menu, screen } from 'electron';
import * as path from 'node:path';
import { rendererLogLine } from './viewutil';

interface RootWindow {
  win: BrowserWindow;
}

// The top-level window that hosts the wasm canvas app. It is a BrowserWindow
// because its web contents auto-fill the window, so the canvas always matches
// its size; a manually-sized WebContentsView drifts under tiling and HiDPI
// window managers such as WSLg. Live url tiles are children of win.contentView.
export function createRootWindow(origin: string): RootWindow {
  // The default menu eats space and, on Linux, renders inside the window.
  Menu.setApplicationMenu(null);

  // The fallback if the window manager ignores the maximize below.
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
      // sandbox:false lets the preload require its sibling ipc module; the
      // renderer is single-tenant, loopback-only and first-party.
      sandbox: false,
    },
  });

  win.maximize();

  // ?e2e=1 makes the wasm client install its read-only window.__gridwellTest.
  const query = process.env.GRIDWELL_E2E === '1' ? '?e2e=1' : '';
  // A rejection needs no notice of ours: no renderer exists yet to draw one,
  // and Chromium paints its own error page in the window.
  void win.loadURL(origin + '/' + query);

  // Forwarding keeps a failure in the app's log after the notice expires off
  // the strip.
  win.webContents.on('console-message', (_e, level, message) => {
    const line = rendererLogLine(level, message);
    if (line) console.error(line);
  });

  // A fullscreen window can keep its old bounds when the display geometry
  // changes. Only while fullscreen: a normal window is the user's to size.
  screen.on('display-metrics-changed', () => {
    if (win.isDestroyed() || !win.isFullScreen()) return;
    const d = screen.getDisplayMatching(win.getBounds());
    win.setBounds(d.bounds);
  });

  // The removed application menu carried the F11 accelerator, so it is bound
  // here, with Ctrl/Cmd+M for a window manager that offers no decoration. Only
  // while the canvas has focus; webviews.ts mirrors F11 for a live view.
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
