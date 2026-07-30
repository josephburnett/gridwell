// Preload bridge: the renderer's only door to the native URL-tile machinery
// in main. Exposed under window.gridwell. The WASM client calls these in
// Phase 3 in place of the old URLStream WebSocket.
import { contextBridge, ipcRenderer } from 'electron';
import {
  CH,
  EV,
  PlaceArgs,
  SetBoundsArgs,
  SetHiddenArgs,
  SetZoomArgs,
  OpenBelowEvent,
  ZoomKeyEvent,
  RemoveArgs,
  PaneRef,
  FreezeResult,
  FrameEvent,
  NavEvent,
  ForwardedRightdown,
  ErrorEvent,
  ShellOpenArgs,
  ShellWriteArgs,
  ShellResizeArgs,
  ShellDataEvent,
  ShellExitEvent,
} from '../main/ipc';

const api = {
  version: 1,
  platform: process.platform,

  placeWebview(args: PlaceArgs): Promise<void> {
    return ipcRenderer.invoke(CH.place, args);
  },
  setBounds(args: SetBoundsArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setBounds, args);
  },
  setHidden(args: SetHiddenArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setHidden, args);
  },
  // setZoom sets the live view's USER content zoom (the tile's persisted
  // content_zoom, issue #82); main composes it with the min-width zoom.
  setZoom(args: SetZoomArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setZoom, args);
  },
  removeWebview(args: RemoveArgs): Promise<FreezeResult> {
    return ipcRenderer.invoke(CH.remove, args);
  },
  goBack(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.goBack, args);
  },
  reload(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.reload, args);
  },

  // Shell transport (2026-07-26): the renderer's xterm speaks to the
  // main-process gRPC OpenShell stream through these four calls plus the
  // onShellData/onShellExit pushes below.
  shellOpen(args: ShellOpenArgs): Promise<void> {
    return ipcRenderer.invoke(CH.shellOpen, args);
  },
  shellWrite(args: ShellWriteArgs): Promise<void> {
    return ipcRenderer.invoke(CH.shellWrite, args);
  },
  shellResize(args: ShellResizeArgs): Promise<void> {
    return ipcRenderer.invoke(CH.shellResize, args);
  },
  shellClose(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.shellClose, args);
  },

  // onFrame/onNav register renderer-side listeners for main→renderer pushes.
  // They return an unsubscribe function.
  onFrame(cb: (ev: FrameEvent) => void): () => void {
    const h = (_e: unknown, ev: FrameEvent) => cb(ev);
    ipcRenderer.on(EV.frame, h);
    return () => ipcRenderer.removeListener(EV.frame, h);
  },
  onNav(cb: (ev: NavEvent) => void): () => void {
    const h = (_e: unknown, ev: NavEvent) => cb(ev);
    ipcRenderer.on(EV.nav, h);
    return () => ipcRenderer.removeListener(EV.nav, h);
  },
  // onRightForward fires when a right-button press lands on a LIVE URL view;
  // main relays it here in canvas coords so the renderer can begin the pane
  // gesture (and then park the view).
  onRightForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.rightForward, h);
    return () => ipcRenderer.removeListener(EV.rightForward, h);
  },
  // onMiddleForward fires when a middle-button press lands on a LIVE URL view;
  // main relays it here in canvas coords so the renderer can ascend the pane.
  onMiddleForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.middleForward, h);
    return () => ipcRenderer.removeListener(EV.middleForward, h);
  },
  // onLeftForward fires when a left-button press lands on a LIVE URL view; main
  // relays it here in canvas coords so the renderer can transfer pane focus. The
  // click is NOT prevented in the preload — in-page interaction stays with the
  // page. Only the focus transfer runs in the renderer.
  onLeftForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.leftForward, h);
    return () => ipcRenderer.removeListener(EV.leftForward, h);
  },
  // onOpenBelow fires when a live view's page tried to open a NEW WINDOW
  // (target=_blank, window.open, ctrl/cmd-click); the wasm splits the pane
  // and opens the url as an ephemeral visit below (issue #111).
  onOpenBelow(cb: (ev: OpenBelowEvent) => void): () => void {
    const h = (_e: unknown, ev: OpenBelowEvent) => cb(ev);
    ipcRenderer.on(EV.openBelow, h);
    return () => ipcRenderer.removeListener(EV.openBelow, h);
  },
  // onZoomKey fires when the content-zoom chord was pressed while a live
  // view owned OS keyboard focus (issue #170); the wasm zoom owner applies
  // and persists it.
  onZoomKey(cb: (ev: ZoomKeyEvent) => void): () => void {
    const h = (_e: unknown, ev: ZoomKeyEvent) => cb(ev);
    ipcRenderer.on(EV.zoomKey, h);
    return () => ipcRenderer.removeListener(EV.zoomKey, h);
  },
  // onShellData delivers PTY output for a pane's terminal.
  onShellData(cb: (ev: ShellDataEvent) => void): () => void {
    const h = (_e: unknown, ev: ShellDataEvent) => cb(ev);
    ipcRenderer.on(EV.shellData, h);
    return () => ipcRenderer.removeListener(EV.shellData, h);
  },
  // onShellExit fires exactly once when a pane's shell stream ends —
  // whatever ended it (clean close, remote hangup, error).
  onShellExit(cb: (ev: ShellExitEvent) => void): () => void {
    const h = (_e: unknown, ev: ShellExitEvent) => cb(ev);
    ipcRenderer.on(EV.shellExit, h);
    return () => ipcRenderer.removeListener(EV.shellExit, h);
  },
  // onError fires for every main-process failure that must reach the user
  // (webview lifecycle, shell stream, sidecar boot/exit) — the
  // one channel the wasm client feeds into its error surface.
  onError(cb: (ev: ErrorEvent) => void): () => void {
    const h = (_e: unknown, ev: ErrorEvent) => cb(ev);
    ipcRenderer.on(EV.error, h);
    return () => ipcRenderer.removeListener(EV.error, h);
  },
};

export type GridwellBridge = typeof api;

contextBridge.exposeInMainWorld('gridwell', api);
