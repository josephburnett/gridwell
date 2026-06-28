import { app } from 'electron';
import { startSidecar, Sidecar } from './sidecar';
import { createRootWindow } from './window';
import { WebviewRegistry } from './webviews';
import { registerWebviewIpc, makeNavForwarder, sendFrame } from './register';
import { MirrorPump } from './capture';
import { sanitizeUserAgent } from './viewutil';

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

async function boot(): Promise<void> {
  // Drop the Electron/app tokens from the default UA before any view loads, so
  // every url tile (all partitions) presents as plain Chrome. userAgentFallback
  // is the UA used when none is set per-webContents, i.e. our default.
  app.userAgentFallback = sanitizeUserAgent(app.userAgentFallback, app.getName());
  try {
    sidecar = await startSidecar();
  } catch (err) {
    console.error('[gridwell] sidecar failed to start:', err);
    app.exit(1);
    return;
  }
  const { win } = createRootWindow(sidecar.origin);
  const rootWC = win.webContents;
  const reg = new WebviewRegistry(win, sidecar.origin, { onNav: makeNavForwarder(rootWC) });
  registry = reg;
  registerWebviewIpc(reg, rootWC, win);

  // Under the e2e harness only (GRIDWELL_E2E=1), expose the registry so a
  // Playwright spec running in the main process can place a real live URL view
  // and drive the native context-menu path. The canvas-only harness can't reach
  // a WebContentsView (it's a separate webContents off the main page), so this
  // is the seam that lets the right-click-menu fix be tested end to end. Inert
  // in every normal launch — mirrors the renderer's ?e2e=1 hook gate.
  if (process.env.GRIDWELL_E2E === '1') {
    (globalThis as { __gwRegistry?: WebviewRegistry }).__gwRegistry = reg;
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
  if (pump) {
    pump.stop();
    pump = null;
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
