// Phase 1 preload: intentionally minimal. Phase 2 exposes the
// window.gridwell.* bridge (placeWebview / setBounds / removeWebview /
// onFrame / onNav) here via contextBridge. For now it only marks that the
// renderer is running inside the Electron shell, which the WASM client can
// later branch on to choose the native-webview URL path.
import { contextBridge } from 'electron';

contextBridge.exposeInMainWorld('gridwellShell', {
  version: 1,
  platform: process.platform,
});
