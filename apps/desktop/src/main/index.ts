import { app, BrowserWindow, dialog, session } from 'electron';
import { startSidecar, Sidecar } from './sidecar';
import { createRootWindow } from './window';
import { WebviewRegistry } from './webviews';
import { registerWebviewIpc, makeNavForwarder, makeOpenBelowForwarder, makeFreezeURLForwarder, makeContextMenuForwarder, makeZoomKeyForwarder, sendFrame, sendError } from './register';
import { MirrorPump } from './capture';
import { sanitizeUserAgent, allowPermission, SESSION_PARTITION } from './viewutil';
import { applyUserDataOverride } from './userdata';
import { sidecarExitMessage } from './sidecar-messages';
import { AUTH_COOKIE_NAME, AUTH_COOKIE_MAX_AGE_S } from './authconst';
import { QuitFlush } from './quit';

// See userdata.ts. The e2e fixture also passes --user-data-dir as a Chromium
// switch; this covers a launch that sets GRIDWELL_HOME without it.
applyUserDataOverride((name, value) => app.setPath(name as Parameters<typeof app.setPath>[0], value), process.env);

// Chromium 137 dropped the silent SwiftShader fallback, so without this a
// GPU-less display (WSLg, xvfb) has no WebGL2 and shell text picks up
// artifacts from the terminal's canvas fallback.
app.commandLine.appendSwitch('enable-unsafe-swiftshader');

// How often live views are captured so other panes showing the same tile
// mirror them; see MirrorPump in capture.ts.
const MIRROR_INTERVAL_MS = 250;

// Gridwell desktop entry.

let sidecar: Sidecar | null = null;
let registry: WebviewRegistry | null = null;
let pump: MirrorPump | null = null;
// quitting keeps the SIGTERM before-quit sends from surfacing as a crash.
let quitting = false;

async function boot(): Promise<void> {
  // Before any view loads, so every url tile presents as plain Chrome.
  app.userAgentFallback = sanitizeUserAgent(app.userAgentFallback, app.getName());
  // allowPermission owns the decision; the deny covers every session.
  const denyExternal = (ses: Electron.Session) =>
    ses.setPermissionRequestHandler((_wc, permission, callback) => {
      callback(allowPermission(permission));
    });
  denyExternal(session.defaultSession);
  denyExternal(session.fromPartition(SESSION_PARTITION));
  // No webContents may spawn a window; the registry replaces this for a live
  // view. Gridwell never calls window.open, so a call here is a page leaving.
  app.on('web-contents-created', (_ev, wc) => {
    wc.setWindowOpenHandler(() => ({ action: 'deny' }));
  });
  try {
    // Never start a server; discover a separately-run one.
    const noServer = process.argv.includes('--no-server') || process.env.GRIDWELL_NO_SERVER === '1';
    sidecar = await startSidecar({ noServer });
  } catch (err) {
    // No renderer exists yet to draw a notice strip, and without a dialog the
    // app would vanish with no explanation.
    const message = err instanceof Error ? err.message : String(err);
    console.error('[gridwell] sidecar failed to start:', err);
    dialog.showErrorBox('Gridwell failed to start', message);
    app.exit(1);
    return;
  }
  // The web password gates other browsers on a shared origin and must never
  // prompt this window, so the banner's token becomes the cookie before the
  // first load. Cookies scope by host, so it survives ephemeral-port churn.
  if (sidecar.auth) {
    await session.defaultSession.cookies.set({
      url: sidecar.origin,
      name: AUTH_COOKIE_NAME,
      value: sidecar.auth,
      // See authconst.ts.
      expirationDate: Math.floor(Date.now() / 1000) + AUTH_COOKIE_MAX_AGE_S,
      httpOnly: true,
      sameSite: 'lax',
    });
  }
  const { win } = createRootWindow(sidecar.origin);
  const rootWC = win.webContents;
  const reg = new WebviewRegistry(win, {
    onNav: makeNavForwarder(rootWC),
    onError: (ev) => sendError(rootWC, ev.source, ev.message),
    onOpenBelow: makeOpenBelowForwarder(rootWC),
    onFreezeURL: makeFreezeURLForwarder(rootWC),
    onContextMenu: makeContextMenuForwarder(rootWC),
    onZoomKey: makeZoomKeyForwarder(rootWC),
    // Give focus back to the root renderer, where the user was typing.
    onFocusStolen: () => rootWC.focus(),
  });
  registry = reg;
  registerWebviewIpc(reg, rootWC, win);

  // startSidecar's own exit listener stops at boot, so without this one a later
  // crash leaves the app a zombie: window open, backend gone. An external
  // server's probe child has already exited by design, so watching it would
  // report a phantom crash.
  if (!sidecar.external) {
    sidecar.child.on('exit', (code, signal) => {
      if (quitting) return;
      sendError(rootWC, 'electron:backend', sidecarExitMessage(code, signal));
    });
  }

  // A WebContentsView is a separate webContents the canvas-only harness cannot
  // reach, so a spec drives the registry directly. __gwSidecarPid lets the
  // fixture assert the sidecar exited.
  if (process.env.GRIDWELL_E2E === '1') {
    (globalThis as { __gwRegistry?: WebviewRegistry; __gwSidecarPid?: number }).__gwRegistry = reg;
    (globalThis as { __gwSidecarPid?: number }).__gwSidecarPid = sidecar.child.pid;
  }

  // Each frame lands in the tile's preview cache, and so in every frozen pane
  // showing it.
  pump = new MirrorPump(MIRROR_INTERVAL_MS, async () => {
    for (const paneId of reg.paneIds()) {
      const jpeg = await reg.capture(paneId);
      const tileId = reg.tileIdFor(paneId);
      if (jpeg && tileId !== undefined) {
        sendFrame(rootWC, paneId, tileId, jpeg);
      }
    }
  });
  pump.start();
}

// One app instance per user, alongside the server's per-home flock: a second
// launch hands off and exits. The e2e harness skips it and runs many instances
// at once, each with its own home and user-data dir.
if (process.env.GRIDWELL_E2E !== '1' && !app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on('second-instance', () => {
    const [win] = BrowserWindow.getAllWindows();
    if (win) {
      if (win.isMinimized()) win.restore();
      win.focus();
    }
  });
  app.whenReady().then(boot);
}

// On macOS the dock can reopen a window, so the process stays alive with none.
app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

// quit.ts owns the sequence and its watchdog; this is the Electron end of it.
const quitFlush = new QuitFlush({
  closeWindows: async () => {
    await Promise.all(
      BrowserWindow.getAllWindows().map(
        (w) =>
          new Promise<void>((res) => {
            if (w.isDestroyed()) return res();
            w.once('closed', () => res());
            w.close();
          }),
      ),
    );
  },
  stopMirror: () => {
    if (pump) {
      pump.stop();
      pump = null;
    }
  },
  removeAll: () => {
    const reg = registry;
    registry = null;
    return reg ? reg.removeAll() : Promise.resolve();
  },
  stopSidecar: () => {
    if (sidecar) {
      sidecar.stop();
      sidecar = null;
    }
  },
  quit: () => app.quit(),
});

app.on('before-quit', (e) => {
  quitting = true;
  if (quitFlush.flushed) return;
  e.preventDefault();
  quitFlush.begin();
});
