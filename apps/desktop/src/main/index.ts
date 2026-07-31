import { app, dialog } from 'electron';
import { startSidecar, Sidecar } from './sidecar';
import { createRootWindow } from './window';
import { WebviewRegistry } from './webviews';
import { registerWebviewIpc, registerShellIpc, makeNavForwarder, makeOpenBelowForwarder, makeZoomKeyForwarder, sendFrame, sendError, sendShellData, sendShellExit } from './register';
import { ShellStreams } from './shellstreams';
import { makeShellDialer } from './shellgrpc';
import { dataProtoPath } from './paths';
import { MirrorPump } from './capture';
import { sanitizeUserAgent } from './viewutil';
import { applyUserDataOverride } from './userdata';
import { sidecarExitMessage } from './sidecar-messages';

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
// Single-tenant, loopback-only: there is no remote endpoint and no auth.

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
  try {
    sidecar = await startSidecar();
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
  const { win } = createRootWindow(sidecar.origin);
  const rootWC = win.webContents;
  const reg = new WebviewRegistry(win, {
    onNav: makeNavForwarder(rootWC),
    onError: (ev) => sendError(rootWC, ev.source, ev.message),
    onOpenBelow: makeOpenBelowForwarder(rootWC),
    onZoomKey: makeZoomKeyForwarder(rootWC),
    // A live view stole focus via page-initiated navigation (issue #172):
    // give it back to the root renderer, where the user was typing.
    onFocusStolen: () => rootWC.focus(),
  });
  registry = reg;
  registerWebviewIpc(reg, rootWC, win);

  // The shell transport: PTY bytes ride a main-process gRPC OpenShell stream
  // to the sidecar's export port and cross to the renderer's xterm over IPC
  // (2026-07-26 — the /rpc/ShellStream WS bridge is gone). A dial failure
  // surfaces on the ONE error wire like every other main-process failure.
  shells = new ShellStreams(
    makeShellDialer(`127.0.0.1:${sidecar.port}`, dataProtoPath()),
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
  sidecar.child.on('exit', (code, signal) => {
    if (quitting) return;
    sendError(rootWC, 'electron:backend', sidecarExitMessage(code, signal));
  });

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

app.whenReady().then(boot);

// Keep the process alive while the sidecar runs even if all windows close;
// on macOS the dock can reopen a window. (Window recreation is Phase 5
// polish; for now quitting is fine on non-darwin.)
app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

app.on('before-quit', () => {
  quitting = true;
  if (pump) {
    pump.stop();
    pump = null;
  }
  if (shells) {
    shells.closeAll();
    shells = null;
  }
  if (registry) {
    void registry.removeAll();
    registry = null;
  }
  if (sidecar) {
    sidecar.stop();
    sidecar = null;
  }
});
