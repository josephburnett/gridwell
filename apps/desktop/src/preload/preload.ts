// The renderer's only door to the native url-tile machinery in main, exposed as
// window.gridwell and called by client/wasm/webview_bridge.go. ipc.ts owns the
// channels and the payload shapes.
import { contextBridge, ipcRenderer } from 'electron';
import {
  CH,
  EV,
  PlaceArgs,
  SetBoundsArgs,
  SetHiddenArgs,
  SetZoomArgs,
  OpenBelowEvent,
  FreezeURLEvent,
  ContextMenuEvent,
  ChoiceMenuArgs,
  ZoomKeyEvent,
  RemoveArgs,
  PaneRef,
  FreezeResult,
  FrameEvent,
  NavEvent,
  ForwardedRightdown,
  ErrorEvent,
} from '../main/ipc';

const api = {
  version: 1,
  // Which parts of the bridge this preload implements. caps.Derive reads it, so
  // exposing the bridge does not imply every native feature.
  caps: { liveUrl: true, choiceMenu: true },

  placeWebview(args: PlaceArgs): Promise<void> {
    return ipcRenderer.invoke(CH.place, args);
  },
  setBounds(args: SetBoundsArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setBounds, args);
  },
  setHidden(args: SetHiddenArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setHidden, args);
  },
  // The tile's persisted content_zoom; main composes it with the min-width zoom.
  setZoom(args: SetZoomArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setZoom, args);
  },
  removeWebview(args: RemoveArgs): Promise<FreezeResult> {
    return ipcRenderer.invoke(CH.remove, args);
  },
  goBack(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.goBack, args);
  },
  // The bar circle's right-click over a live url, with no in-page context.
  showMenu(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.showMenu, args);
  },
  // A native menu of the choices the renderer declares, answering with the
  // chosen id or null. The renderer owns what the rows mean.
  showChoiceMenu(args: ChoiceMenuArgs): Promise<string | null> {
    return ipcRenderer.invoke(CH.choiceMenu, args);
  },


  // Every on* here returns an unsubscribe function.
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
  // A right-button press on a live url view, in canvas coords, so the renderer
  // begins the pane gesture and parks the view.
  onRightForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.rightForward, h);
    return () => ipcRenderer.removeListener(EV.rightForward, h);
  },
  // A middle-button press, so the renderer ascends the pane.
  onMiddleForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.middleForward, h);
    return () => ipcRenderer.removeListener(EV.middleForward, h);
  },
  // A left-button press, so the renderer transfers pane focus. The click is not
  // prevented, so in-page interaction stays with the page.
  onLeftForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.leftForward, h);
    return () => ipcRenderer.removeListener(EV.leftForward, h);
  },
  // The wasm splits the pane and opens the url as an ephemeral visit below.
  onOpenBelow(cb: (ev: OpenBelowEvent) => void): () => void {
    const h = (_e: unknown, ev: OpenBelowEvent) => cb(ev);
    ipcRenderer.on(EV.openBelow, h);
    return () => ipcRenderer.removeListener(EV.openBelow, h);
  },
  onFreezeURL(cb: (ev: FreezeURLEvent) => void): () => void {
    const h = (_e: unknown, ev: FreezeURLEvent) => cb(ev);
    ipcRenderer.on(EV.freezeUrl, h);
    return () => ipcRenderer.removeListener(EV.freezeUrl, h);
  },
  // Just before a context menu opens, so the wasm moves focus to that pane.
  onContextMenu(cb: (ev: ContextMenuEvent) => void): () => void {
    const h = (_e: unknown, ev: ContextMenuEvent) => cb(ev);
    ipcRenderer.on(EV.menuPane, h);
    return () => ipcRenderer.removeListener(EV.menuPane, h);
  },
  onZoomKey(cb: (ev: ZoomKeyEvent) => void): () => void {
    const h = (_e: unknown, ev: ZoomKeyEvent) => cb(ev);
    ipcRenderer.on(EV.zoomKey, h);
    return () => ipcRenderer.removeListener(EV.zoomKey, h);
  },
  // The one channel the wasm client feeds into its error surface.
  onError(cb: (ev: ErrorEvent) => void): () => void {
    const h = (_e: unknown, ev: ErrorEvent) => cb(ev);
    ipcRenderer.on(EV.error, h);
    return () => ipcRenderer.removeListener(EV.error, h);
  },
};

contextBridge.exposeInMainWorld('gridwell', api);
