import { ipcMain, BaseWindow, WebContents } from 'electron';
import {
  CH,
  CTRL,
  EV,
  VIEW,
  PlaceArgs,
  SetBoundsArgs,
  SetHiddenArgs,
  RemoveArgs,
  PaneRef,
  FreezeResult,
  ViewRightdown,
} from './ipc';
import { WebviewRegistry } from './webviews';

// registerWebviewIpc connects the renderer-facing IPC channels to the
// registry. Call once after the root window is created. rootWC is the
// renderer's web contents; win is the root window (its content bounds convert
// a live view's screen-space press into renderer/canvas coordinates).
export function registerWebviewIpc(
  registry: WebviewRegistry,
  rootWC: WebContents,
  win: BaseWindow,
): void {
  // Corner-button overlay click: resolve the sender view back to its pane,
  // then route left→back (handled natively) and right/middle→ascend (the
  // ascent animation lives in the renderer, so forward it there).
  ipcMain.on(CTRL.click, (event, button: number): void => {
    const paneId = registry.controlPaneFor(event.sender.id);
    if (!paneId) return;
    if (button === 0) {
      registry.goBack(paneId);
    } else if (!rootWC.isDestroyed()) {
      rootWC.send(EV.controlAscend, { paneId });
    }
  });

  // A live URL view's preload forwards a right-button press here so the
  // renderer can begin a pane gesture over live content. The press arrives in
  // physical screen coords; subtract the window's content origin to get
  // renderer/canvas coords, then relay to the renderer (which starts the
  // gesture and parks the view so the rest of the drag lands on the canvas).
  ipcMain.on(VIEW.rightdown, (_event, p: ViewRightdown): void => {
    if (rootWC.isDestroyed()) return;
    const cb = win.getContentBounds();
    rootWC.send(EV.rightForward, { x: p.sx - cb.x, y: p.sy - cb.y });
  });

  // A middle-button press over a live URL view is the ascend gesture; the
  // native view swallows it, so its preload forwards it here. Relay to the
  // renderer in canvas coords, where it resolves the pane and ascends.
  ipcMain.on(VIEW.middledown, (_event, p: ViewRightdown): void => {
    if (rootWC.isDestroyed()) return;
    const cb = win.getContentBounds();
    rootWC.send(EV.middleForward, { x: p.sx - cb.x, y: p.sy - cb.y });
  });

  ipcMain.handle(CH.place, (_e, a: PlaceArgs): Promise<void> => {
    return registry.place(a.paneId, a.tileId, a.objectId, a.url, a.bounds, a.pluginUuid);
  });

  ipcMain.handle(CH.setBounds, (_e, a: SetBoundsArgs): void => {
    registry.setBounds(a.paneId, a.bounds);
  });

  ipcMain.handle(CH.setHidden, (_e, a: SetHiddenArgs): void => {
    registry.setHidden(a.paneId, a.hidden, a.focused);
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
