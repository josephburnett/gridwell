import { ipcMain, BaseWindow, WebContents } from 'electron';
import {
  CH,
  EV,
  VIEW,
  PlaceArgs,
  SetBoundsArgs,
  SetHiddenArgs,
  SetZoomArgs,
  RemoveArgs,
  PaneRef,
  FreezeResult,
  ViewRightdown,
  ForwardedRightdown,
  ErrorEvent,
  OpenBelowEvent,
  FreezeURLEvent,
  ZoomKeyEvent,
  ShellOpenArgs,
  ShellWriteArgs,
  ShellResizeArgs,
} from './ipc';
import { WebviewRegistry } from './webviews';
import { ShellStreams } from './shellstreams';

// safeSend is the one guard every main→renderer push goes through: the
// window can close mid-flight (quit, crash, boot failure racing teardown),
// and calling .send on a destroyed WebContents throws. One owner instead of
// each call site repeating its own isDestroyed() check (charter §8 — this
// used to be duplicated three times across makeNavForwarder/sendFrame/the
// control-click handler; sendError below would have made a fourth).
function safeSend(wc: WebContents, channel: string, payload: unknown): void {
  if (!wc.isDestroyed()) wc.send(channel, payload);
}

// registerShellIpc connects the renderer's shell-transport channels to the
// ShellStreams registry (the gRPC OpenShell relay). Call once after boot.
export function registerShellIpc(streams: ShellStreams): void {
  ipcMain.handle(CH.shellOpen, (_e, args: ShellOpenArgs): void => {
    streams.open(args.paneId, args.tileId, args.cols, args.rows);
  });
  ipcMain.handle(CH.shellWrite, (_e, args: ShellWriteArgs): void => {
    streams.write(args.paneId, args.data);
  });
  ipcMain.handle(CH.shellResize, (_e, args: ShellResizeArgs): void => {
    streams.resize(args.paneId, args.cols, args.rows);
  });
  ipcMain.handle(CH.shellClose, (_e, args: PaneRef): void => {
    streams.close(args.paneId);
  });
}

// sendShellData / sendShellExit are the main→renderer pushes for the shell
// transport, through the same safeSend guard as every other push.
export function sendShellData(wc: WebContents, paneId: string, data: Uint8Array): void {
  safeSend(wc, EV.shellData, { paneId, data });
}
export function sendShellExit(wc: WebContents, paneId: string, message: string, sessionGone: boolean): void {
  safeSend(wc, EV.shellExit, { paneId, message, sessionGone });
}

// registerWebviewIpc connects the renderer-facing IPC channels to the
// registry. Call once after the root window is created. rootWC is the
// renderer's web contents; win is the root window (its content bounds convert
// a live view's screen-space press into renderer/canvas coordinates).
export function registerWebviewIpc(
  registry: WebviewRegistry,
  rootWC: WebContents,
  win: BaseWindow,
): void {
  // A live URL view's preload forwards a right-button press here so the
  // renderer can begin a pane gesture over live content. The press arrives in
  // physical screen coords; subtract the window's content origin to get
  // renderer/canvas coords, then relay to the renderer (which starts the
  // gesture and parks the view so the rest of the drag lands on the canvas).
  ipcMain.on(VIEW.rightdown, (_event, p: ViewRightdown): void => {
    const cb = win.getContentBounds();
    safeSend(rootWC, EV.rightForward, { x: p.sx - cb.x, y: p.sy - cb.y });
  });

  // A middle-button press over a live URL view is the ascend gesture; the
  // native view swallows it, so its preload forwards it here. Relay to the
  // renderer in canvas coords, where it resolves the pane and ascends.
  ipcMain.on(VIEW.middledown, (_event, p: ViewRightdown): void => {
    const cb = win.getContentBounds();
    safeSend(rootWC, EV.middleForward, { x: p.sx - cb.x, y: p.sy - cb.y });
  });

  // A left-button press over a live URL view is a focus-transfer intent; the
  // native WebContentsView swallows the canvas's own mousedown, so the preload
  // forwards a (non-suppressed) left-down here. Relay to the renderer in canvas
  // coords so it can call focusToPane without breaking in-page interaction.
  ipcMain.on(VIEW.leftdown, (event, p: ViewRightdown): void => {
    // The press is the one legitimate path to OS focus for a live view —
    // stamp it so the focus guard doesn't bounce the click (issue #172).
    registry.noteUserClick(event.sender);
    const cb = win.getContentBounds();
    const fwd: ForwardedRightdown = { x: p.sx - cb.x, y: p.sy - cb.y };
    safeSend(rootWC, EV.leftForward, fwd);
  });

  ipcMain.handle(CH.place, (_e, a: PlaceArgs): Promise<void> => {
    return registry.place(a.paneId, a.tileId, a.objectId, a.url, a.bounds, a.contentZoom ?? 0, a.history ?? '', a.durable ?? false);
  });

  ipcMain.handle(CH.setZoom, (_e, a: SetZoomArgs): void => {
    registry.setZoom(a.paneId, a.zoom);
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

  ipcMain.handle(CH.showMenu, (_e, a: PaneRef): void => {
    registry.showMenu(a.paneId);
  });

}

// makeNavForwarder returns a registry onNav callback that ships nav events
// to the renderer over EV.nav.
export function makeNavForwarder(rootWC: WebContents) {
  return (ev: { paneId: string; tileId: number; url: string; title: string }) => {
    safeSend(rootWC, EV.nav, ev);
  };
}

// makeOpenBelowForwarder relays a live view's new-window link to the renderer
// (EV.openBelow), which splits the pane and opens it ephemeral (issue #111).
export function makeOpenBelowForwarder(rootWC: WebContents): (ev: OpenBelowEvent) => void {
  return (ev) => safeSend(rootWC, EV.openBelow, ev);
}

// makeFreezeURLForwarder relays the context menu's explicit freeze gesture
// (issue #237) to the renderer (EV.freezeUrl), where the wasm tears the view
// down and persists the standing frozen intent.
export function makeFreezeURLForwarder(rootWC: WebContents): (ev: FreezeURLEvent) => void {
  return (ev) => safeSend(rootWC, EV.freezeUrl, ev);
}

// makeZoomKeyForwarder relays the content-zoom chord from a focused live view
// to the renderer (EV.zoomKey), where the one zoom owner applies + persists it
// (issue #170).
export function makeZoomKeyForwarder(rootWC: WebContents): (ev: ZoomKeyEvent) => void {
  return (ev) => safeSend(rootWC, EV.zoomKey, ev);
}

// sendFrame ships a mirror/capture frame to the renderer.
export function sendFrame(rootWC: WebContents, paneId: string, tileId: number, jpegBase64: string): void {
  if (jpegBase64) safeSend(rootWC, EV.frame, { paneId, tileId, jpegBase64 });
}

// sendError is the ONE main-process entry point onto EV.error (issue #46):
// every failure site — webview lifecycle, shell stream, sidecar
// boot/exit — calls this instead of console.error-and-return, so the wasm
// errsurface (client/errsurface) is the single place failures become visible.
export function sendError(rootWC: WebContents, source: string, message: string): void {
  // Also the log line: a main-process failure must reach the app's log even
  // when the renderer is gone (safeSend no-ops on a destroyed webContents).
  console.error(`[gridwell] ${source}: ${message}`);
  const ev: ErrorEvent = { source, message };
  safeSend(rootWC, EV.error, ev);
}
