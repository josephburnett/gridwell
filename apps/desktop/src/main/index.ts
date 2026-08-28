import { app, BrowserWindow, dialog, session } from 'electron';
import { startSidecar, Sidecar } from './sidecar';
import { createRootWindow } from './window';
import { WebviewRegistry } from './webviews';
import { registerWebviewIpc, registerShellIpc, makeNavForwarder, makeOpenBelowForwarder, makeFreezeURLForwarder, makeZoomKeyForwarder, sendFrame, sendError, sendShellData, sendShellExit } from './register';
import { ShellStreams } from './shellstreams';
import { makeShellDialer } from './shellgrpc';
import { dataProtoPath } from './paths';
import { MirrorPump } from './capture';
import { sanitizeUserAgent, allowPermission, SESSION_PARTITION } from './viewutil';
import { applyUserDataOverride } from './userdata';
import { sidecarExitMessage } from './sidecar-messages';
import { AUTH_COOKIE_NAME, AUTH_COOKIE_MAX_AGE_S } from './authconst';

// Belt: redirect Electron's userData (and, for Electron ≥28, sessionData) to a
// per-run private directory when GRIDWELL_HOME is set.
//
// The e2e fixture ALSO passes --user-data-dir=<home>/electron as a Chromium
// command-line switch (fixtures.ts), which is more reliable under Playwright's
// loader.js interception of app.isReady. This call is the fallback for any
// non-Playwright launch where GRIDWELL_HOME is set without the CLI flag.
//
// When GRIDWELL_HOME is absent (normal user launch) this is a no-op and
// Electron keeps its default ~/.config/gridwell-desktop profile.
applyUserDataOverride((name, value) => app.setPath(name as Parameters<typeof app.setPath>[0], value), process.env);

// Chromium ≥137 removed the SILENT SwiftShader fallback for WebGL — software
// WebGL now needs this switch. Without it, any GPU-less display (WSLg, xvfb)
// has no WebGL2, the terminal's WebGL renderer silently falls back to the
// legacy canvas renderer, and the #84 text-artifact class returns (issue
// #128; this is a restore of the Electron-33-era behavior the terminal
// shipped and was tested on, not a new exposure).
app.commandLine.appendSwitch('enable-unsafe-swiftshader');

// MIRROR_INTERVAL_MS is how often live views are captured and their frames
// pushed to the renderer so OTHER panes showing the same tile mirror live
// navigation (the preview = descent = ascent invariant, live edition). The
// live pane itself renders natively and ignores these frames. Modest by
// design — mirrored previews don't need 60fps, and capturePage is not free.
const MIRROR_INTERVAL_MS = 250;

// Gridwell desktop entry. Boot order:
//   1. spawn the Go sidecar and wait for it to listen
//   2. open the root window pointing at the sidecar's loopback origin
//   3. tear the sidecar down on quit
//
// Single-tenant. The web door is always password-gated (the minted
// web-password file); this window authenticates itself from the serve
// banner's token (see boot below) and never prompts.

let sidecar: Sidecar | null = null;
let registry: WebviewRegistry | null = null;
let pump: MirrorPump | null = null;
let shells: ShellStreams | null = null;
// quitting guards the post-boot sidecar exit listener below: before-quit
// stops the sidecar itself (SIGTERM), and that expected exit must not surface
// as a "backend crashed, restart the app" notice while the window is already
// closing.
let quitting = false;

async function boot(): Promise<void> {
  // Drop the Electron/app tokens from the default UA before any view loads, so
  // every url tile (all partitions) presents as plain Chrome. userAgentFallback
  // is the UA used when none is set per-webContents, i.e. our default.
  app.userAgentFallback = sanitizeUserAgent(app.userAgentFallback, app.getName());
  // NOTHING escapes to the OS (issues #232/#246): Electron grants the
  // 'openExternal' permission by default, so a page navigating to a
  // non-web protocol launched the platform opener (on WSL, xdg-open →
  // the WINDOWS default browser). #232 denied it on the live-view
  // partition only; the deny is now app-wide — EVERY session (the root
  // renderer's default session included), because the invariant is about
  // the app, not one partition: a tile is Gridwell's only browsing
  // surface. allowPermission (unit-tested) decides; everything else
  // keeps the default grant.
  const denyExternal = (ses: Electron.Session) =>
    ses.setPermissionRequestHandler((_wc, permission, callback) => {
      callback(allowPermission(permission));
    });
  denyExternal(session.defaultSession);
  denyExternal(session.fromPartition(SESSION_PARTITION));
  // And no webContents may spawn a window: live views get their own
  // open-below handler when the registry places them (a later
  // setWindowOpenHandler replaces this one); everything else — the root
  // renderer, preloads, anything future — denies outright. Gridwell code
  // never calls window.open, so a call reaching this default is a page
  // (or a library) trying to leave.
  app.on('web-contents-created', (_ev, wc) => {
    wc.setWindowOpenHandler(() => ({ action: 'deny' }));
  });
  try {
    // --no-server / GRIDWELL_NO_SERVER: never start a server — discover a
    // separately-run one via `gridwell status` (the advanced split; the
    // server owns the per-home lock and the discovery banner).
    const noServer = process.argv.includes('--no-server') || process.env.GRIDWELL_NO_SERVER === '1';
    sidecar = await startSidecar({ noServer });
  } catch (err) {
    // No renderer exists yet at this point (the window is created below, only
    // after the sidecar is up), so there is nowhere to draw a notice-strip
    // error into. dialog.showErrorBox is the one surface available before
    // boot — previously this was console.error + a silent app.exit(1), so a
    // boot failure (missing binary, bad server.yaml, port in use) made the
    // app vanish with zero explanation (issue #46 point 2).
    const message = err instanceof Error ? err.message : String(err);
    console.error('[gridwell] sidecar failed to start:', err);
    dialog.showErrorBox('Gridwell failed to start', message);
    app.exit(1);
    return;
  }
  // The web-UI password must never prompt THIS window — the gate is for
  // other browsers on a shared origin. The
  // serve banner carries the derived auth token; set it as the cookie the
  // server checks, before the window's first load. Cookies scope by host,
  // not port, so it also survives the ephemeral-port churn across launches.
  if (sidecar.auth) {
    await session.defaultSession.cookies.set({
      url: sidecar.origin,
      name: AUTH_COOKIE_NAME,
      value: sidecar.auth,
      // Mirror the server's own cookie shape (server/auth.go, pinned by
      // authconst.test.ts); re-set on every boot anyway.
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
    onZoomKey: makeZoomKeyForwarder(rootWC),
    // A live view stole focus via page-initiated navigation (issue #172):
    // give it back to the root renderer, where the user was typing.
    onFocusStolen: () => rootWC.focus(),
  });
  registry = reg;
  registerWebviewIpc(reg, rootWC, win);

  // The shell transport: PTY bytes ride a main-process gRPC OpenShell stream
  // to the sidecar's federation socket and cross to the renderer's xterm over IPC
  // (2026-07-26 — the /rpc/ShellStream WS bridge is gone). A dial failure
  // surfaces on the ONE error wire like every other main-process failure.
  shells = new ShellStreams(
    // dialAddr is the banner's federation socket — local, whatever the
    // web door is bound to (a Tailscale IP for the window is fine).
    makeShellDialer(sidecar.dialAddr, dataProtoPath()),
    (paneId, data) => sendShellData(rootWC, paneId, data),
    (ev) => {
      sendShellExit(rootWC, ev.paneId, ev.message, ev.sessionGone);
      if (ev.message !== '' && !ev.sessionGone) {
        sendError(rootWC, 'electron:shell', 'shell stream failed: ' + ev.message);
      }
    },
  );
  registerShellIpc(shells);

  // A PERSISTENT post-boot exit watch (issue #46 point 1): startSidecar's own
  // `child.once('exit', ...)` listener only rejects the boot promise BEFORE it
  // settles — once resolved, that listener still fires on the real exit but
  // no-ops (its `settled` guard), so a crash after boot was completely
  // unobserved and the app became a zombie (window open, backend gone, every
  // RPC hanging/failing with no explanation). This listener is independent
  // (child_process fires 'exit' to every registered listener, not just one),
  // so it sees the same event and reports it — unless we're already quitting,
  // in which case the exit is expected (our own SIGTERM in before-quit).
  // An EXTERNAL server's probe child has already exited by design (it only
  // re-emitted the running holder's banner) — watching it would report a
  // phantom crash for a perfectly healthy backend.
  if (!sidecar.external) {
    sidecar.child.on('exit', (code, signal) => {
      if (quitting) return;
      sendError(rootWC, 'electron:backend', sidecarExitMessage(code, signal));
    });
  }

  // Under the e2e harness only (GRIDWELL_E2E=1), expose the registry so a
  // Playwright spec running in the main process can place a real live URL view
  // and drive the native context-menu path. The canvas-only harness can't reach
  // a WebContentsView (it's a separate webContents off the main page), so this
  // is the seam that lets the right-click-menu fix be tested end to end.
  // __gwSidecarPid lets the fixture assert the sidecar exited after app.close().
  // Both are inert in every normal launch — mirrors the renderer's ?e2e=1 gate.
  if (process.env.GRIDWELL_E2E === '1') {
    (globalThis as { __gwRegistry?: WebviewRegistry; __gwSidecarPid?: number }).__gwRegistry = reg;
    (globalThis as { __gwSidecarPid?: number }).__gwSidecarPid = sidecar.child.pid;
  }

  // Mirror live views to other panes: capture each live view on a modest
  // cadence and push the frame to the renderer, which updates the tile's
  // preview cache (and thus every frozen pane showing it).
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

// ONE app instance per user (Electron's own lock — complementing the
// server's per-home flock): a second launch hands off to the first and
// exits; the first focuses its window. Skipped under the e2e harness,
// where many isolated instances run concurrently on purpose (each has its
// own home + user-data dir; the serve flock still guards each home).
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

// Keep the process alive while the sidecar runs even if all windows close;
// on macOS the dock can reopen a window. (Recreating the window from the
// dock is not implemented; quitting is fine on non-darwin.)
app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

// Quit is TWO-PHASE (audit #2c, 2026-08-14). The old handler tore the
// native views down (removeAll — discarding every FreezeResult) and
// SIGTERMed the sidecar synchronously, all BEFORE any renderer's
// beforeunload ran — so the renderer's unload flush found no live views
// and no server, and quitting the app while a url tile was live lost
// that tile's page, trail, and unsaved text every time. Now the first
// before-quit closes the windows and waits: each renderer's beforeunload
// runs its full flush (text + framing + url-state beacons against a
// still-alive sidecar, bridge IPC against still-alive views); then the
// views' own teardown flushes DOM storage; only then does the sidecar
// stop and the quit proceed. A watchdog caps the wait so a wedged
// renderer can never hold quit hostage.
let quitFlushed = false;
app.on('before-quit', (e) => {
  quitting = true;
  if (quitFlushed) return;
  e.preventDefault();
  const finish = (): void => {
    if (quitFlushed) return;
    quitFlushed = true;
    if (pump) {
      pump.stop();
      pump = null;
    }
    if (shells) {
      shells.closeAll();
      shells = null;
    }
    const reg = registry;
    registry = null;
    const done = (): void => {
      if (sidecar) {
        sidecar.stop();
        sidecar = null;
      }
      app.quit();
    };
    // removeAll still runs for its localStorage flush; its captures are
    // moot — the renderers already beaconed their state.
    if (reg) void reg.removeAll().then(done, done);
    else done();
  };
  const watchdog = setTimeout(finish, 2000);
  const wins = BrowserWindow.getAllWindows();
  void Promise.all(
    wins.map(
      (w) =>
        new Promise<void>((res) => {
          if (w.isDestroyed()) return res();
          w.once('closed', () => res());
          w.close();
        }),
    ),
  ).then(() => {
    clearTimeout(watchdog);
    finish();
  });
});
