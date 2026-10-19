import { BrowserWindow, Menu, screen } from 'electron';
import * as path from 'node:path';
import { rendererLogLine } from './viewutil';
import { logLine, trace } from './trace';
import { windowFocused, windowState, windowStateRecorder } from './viewtrace';

// A read of a destroyed window throws uncaught, which hangs main behind an
// error dialog.
function traceState(win: BrowserWindow, cause: string): void {
  if (win.isDestroyed()) return;
  const bounds = win.getBounds();
  const display = screen.getDisplayMatching(bounds);
  const [minW, minH] = win.getMinimumSize();
  const [maxW, maxH] = win.getMaximumSize();
  trace(
    windowState(cause, {
      resizable: win.isResizable(),
      maximized: win.isMaximized(),
      fullScreen: win.isFullScreen(),
      minimizable: win.isMinimizable(),
      maximizable: win.isMaximizable(),
      bounds,
      workArea: display.workAreaSize,
      scaleFactor: display.scaleFactor,
      platform: process.platform,
      electron: process.versions.electron,
      minSize: [minW, minH],
      maxSize: [maxW, maxH],
    }),
  );
}

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
  const state = windowStateRecorder((cause) => traceState(win, cause));
  state.now('create');

  // ?e2e=1 makes the wasm client install its read-only window.__gridwellTest.
  const query = process.env.GRIDWELL_E2E === '1' ? '?e2e=1' : '';
  // A rejection needs no notice of ours: no renderer exists yet to draw one,
  // and Chromium paints its own error page in the window.
  void win.loadURL(origin + '/' + query);

  // Forwarding keeps a failure in the app's log after the notice expires off
  // the notice row.
  win.webContents.on('console-message', (_e, level, message) => {
    const line = rendererLogLine(level, message);
    if (line) logLine('error', line, 'renderer');
  });

  win.on('focus', () => trace(windowFocused(true)));
  win.on('blur', () => trace(windowFocused(false)));
  for (const cause of ['maximize', 'unmaximize', 'enter-full-screen', 'leave-full-screen'] as const) {
    win.on(cause as 'maximize', () => state.now(cause));
  }
  win.on('resize', () => state.settle('resize'));
  win.on('move', () => state.settle('move'));
  win.on('closed', state.stop);

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
