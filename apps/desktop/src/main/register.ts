import { ipcMain, WebContents } from 'electron';
import {
  CH,
  EV,
  PlaceArgs,
  SetBoundsArgs,
  SetHiddenArgs,
  RemoveArgs,
  PaneRef,
  FreezeResult,
} from './ipc';
import { WebviewRegistry } from './webviews';

// registerWebviewIpc connects the renderer-facing IPC channels to the
// registry. Call once after the root window is created.
export function registerWebviewIpc(registry: WebviewRegistry): void {
  ipcMain.handle(CH.place, (_e, a: PlaceArgs): void => {
    registry.place(a.paneId, a.tileId, a.objectId, a.url, a.bounds);
  });

  ipcMain.handle(CH.setBounds, (_e, a: SetBoundsArgs): void => {
    registry.setBounds(a.paneId, a.bounds);
  });

  ipcMain.handle(CH.setHidden, (_e, a: SetHiddenArgs): void => {
    registry.setHidden(a.paneId, a.hidden);
  });

  ipcMain.handle(CH.remove, async (_e, a: RemoveArgs): Promise<FreezeResult> => {
    return registry.remove(a.paneId);
  });

  ipcMain.handle(CH.goBack, (_e, a: PaneRef): void => {
    registry.goBack(a.paneId);
  });

  ipcMain.handle(CH.reload, (_e, a: PaneRef): void => {
    registry.reload(a.paneId);
  });
}

// makeNavForwarder returns a registry onNav callback that ships nav events
// to the renderer over EV.nav.
export function makeNavForwarder(rootWC: WebContents) {
  return (ev: { paneId: string; tileId: number; url: string; title: string }) => {
    if (!rootWC.isDestroyed()) rootWC.send(EV.nav, ev);
  };
}

// sendFrame ships a mirror/capture frame to the renderer.
export function sendFrame(rootWC: WebContents, paneId: string, tileId: number, jpegBase64: string): void {
  if (jpegBase64 && !rootWC.isDestroyed()) {
    rootWC.send(EV.frame, { paneId, tileId, jpegBase64 });
  }
}
