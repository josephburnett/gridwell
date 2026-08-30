import { BrowserWindow, Menu, screen } from 'electron';
import * as path from 'node:path';
import { rendererLogLine } from './viewutil';

interface RootWindow {
  win: BrowserWindow;
}

// createRootWindow builds the top-level window that hosts the Gridwell
// renderer, the wasm canvas app served by the sidecar.
//
// It is a BrowserWindow, not a bare BaseWindow holding a manually-sized
// WebContentsView: a BrowserWindow's web contents auto-fills the window, so the
// canvas always matches the window size. Manual layout drifts under tiling and
// HiDPI window managers (WSLg, for one), which can leave the canvas at a stale
// size with bare window background showing.
//
// Live url tiles are WebContentsView children added to win.contentView
// (BrowserWindow inherits contentView from BaseWindow) and paint on top of the
// canvas. The WebviewRegistry takes this window unchanged.
export function createRootWindow(origin: string): RootWindow {
  // Gridwell is a mouse-only canvas; the default Electron application menu
  // (File/Edit/View/…) only eats space and, on Linux, renders inside the
  // window. Remove it entirely.
  Menu.setApplicationMenu(null);

  // Size the initial bounds to the primary display's work area so a fresh
  // launch never opens larger than the screen. The window then maximizes; this
  // size is the fallback if the window manager ignores the maximize request.
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

  // GRIDWELL_E2E=1 (set only by the Playwright e2e fixture) loads the renderer
  // with ?e2e=1 so the wasm client installs its read-only window.__gridwellTest
  // introspection surface. Inert in every normal launch.
  const query = process.env.GRIDWELL_E2E === '1' ? '?e2e=1' : '';
  void win.loadURL(origin + '/' + query);

  // Forward renderer console warnings/errors into the main process's own
  // stdout/stderr. The wasm client logs every surfaced errsurface notice to
  // its console (reportErr), so this line is what makes those failures
  // greppable in the app's log after the notice expires off the strip.
  win.webContents.on('console-message', (_e, level, message) => {
    const line = rendererLogLine(level, message);
    if (line) console.error(line);
  });

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

  // Removing the application menu removed its F11 fullscreen accelerator, so
  // bind F11 here, plus Ctrl/Cmd+M to minimize. These act when the canvas has
  // focus; a focused url tile's native view handles its own keys, and
  // webviews.ts mirrors F11 there.
  //
  // Minimize is bound explicitly because a window manager may offer no minimize
  // control in its decorations, leaving no other way down.
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
