import { app } from 'electron';
import { startSidecar, Sidecar } from './sidecar';
import { createRootWindow } from './window';
import { WebviewRegistry } from './webviews';
import { registerWebviewIpc, makeNavForwarder } from './register';

// Gridwell desktop entry. Boot order:
//   1. spawn the Go sidecar (--no-browser) and wait for it to listen
//   2. open the root window pointing at the sidecar's loopback origin
//   3. tear the sidecar down on quit
//
// Single-tenant, loopback-only: there is no remote endpoint and no auth.

let sidecar: Sidecar | null = null;
let registry: WebviewRegistry | null = null;

async function boot(): Promise<void> {
  try {
    sidecar = await startSidecar();
  } catch (err) {
    console.error('[gridwell] sidecar failed to start:', err);
    app.exit(1);
    return;
  }
  const { win, root } = createRootWindow(sidecar.origin);
  registry = new WebviewRegistry(win, { onNav: makeNavForwarder(root.webContents) });
  registerWebviewIpc(registry);
}

app.whenReady().then(boot);

// Keep the process alive while the sidecar runs even if all windows close;
// on macOS the dock can reopen a window. (Window recreation is Phase 5
// polish; for now quitting is fine on non-darwin.)
app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

app.on('before-quit', () => {
  if (registry) {
    void registry.removeAll();
    registry = null;
  }
  if (sidecar) {
    sidecar.stop();
    sidecar = null;
  }
});
