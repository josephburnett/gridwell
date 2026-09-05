// Preload bridge: the renderer's only door to the native url-tile machinery in
// main. Exposed as window.gridwell; the wasm client calls it
// (client/wasm/webview_bridge.go).
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
  // The host's own capability declaration: which parts of the bridge this
  // preload implements. caps.Derive reads it, so exposing the bridge does not
  // imply every native feature. Live url views are the only part; shells ride
  // the web door and need nothing from the host.
  caps: { liveUrl: true },

  placeWebview(args: PlaceArgs): Promise<void> {
    return ipcRenderer.invoke(CH.place, args);
  },
  setBounds(args: SetBoundsArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setBounds, args);
  },
  setHidden(args: SetHiddenArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setHidden, args);
  },
  // setZoom sets the live view's user content zoom (the tile's persisted
  // content_zoom); main composes it with the min-width zoom.
  setZoom(args: SetZoomArgs): Promise<void> {
    return ipcRenderer.invoke(CH.setZoom, args);
  },
  removeWebview(args: RemoveArgs): Promise<FreezeResult> {
    return ipcRenderer.invoke(CH.remove, args);
  },
  goBack(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.goBack, args);
  },
  // showMenu pops the live view's context menu, Freeze Page included, with no
  // in-page context. This is the bar circle's right-click, which stays
  // reachable when the page itself hijacks contextmenu.
  showMenu(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.showMenu, args);
  },


  // onFrame and onNav register renderer-side listeners for main→renderer
  // pushes. Each returns an unsubscribe function.
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
  // onRightForward fires when a right-button press lands on a live url view.
  // Main relays it here in canvas coords so the renderer can begin the pane
  // gesture and then park the view.
  onRightForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.rightForward, h);
    return () => ipcRenderer.removeListener(EV.rightForward, h);
  },
  // onMiddleForward fires when a middle-button press lands on a live url view.
  // Main relays it here in canvas coords so the renderer can ascend the pane.
  onMiddleForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.middleForward, h);
    return () => ipcRenderer.removeListener(EV.middleForward, h);
  },
  // onLeftForward fires when a left-button press lands on a live url view. Main
  // relays it here in canvas coords so the renderer can transfer pane focus.
  // The preload does not prevent the click: in-page interaction stays with the
  // page, and only the focus transfer runs in the renderer.
  onLeftForward(cb: (ev: ForwardedRightdown) => void): () => void {
    const h = (_e: unknown, ev: ForwardedRightdown) => cb(ev);
    ipcRenderer.on(EV.leftForward, h);
    return () => ipcRenderer.removeListener(EV.leftForward, h);
  },
  // onOpenBelow fires when a live view's page tried to open a new window
  // through target=_blank, window.open, or a ctrl/cmd-click. The wasm splits
  // the pane and opens the url as an ephemeral visit below.
  onOpenBelow(cb: (ev: OpenBelowEvent) => void): () => void {
    const h = (_e: unknown, ev: OpenBelowEvent) => cb(ev);
    ipcRenderer.on(EV.openBelow, h);
    return () => ipcRenderer.removeListener(EV.openBelow, h);
  },
  // onFreezeURL fires when the user picked "Freeze Page" in a live view's
  // context menu. The wasm freezes the pane's view and stores the standing
  // intent.
  onFreezeURL(cb: (ev: FreezeURLEvent) => void): () => void {
    const h = (_e: unknown, ev: FreezeURLEvent) => cb(ev);
    ipcRenderer.on(EV.freezeUrl, h);
    return () => ipcRenderer.removeListener(EV.freezeUrl, h);
  },
  // onContextMenu fires just before a live view's context menu opens, naming
  // the pane it acts in. The wasm moves focus to that pane: right-clicking a
  // pane is interacting with it, the same rule a left-click obeys.
  onContextMenu(cb: (ev: ContextMenuEvent) => void): () => void {
    const h = (_e: unknown, ev: ContextMenuEvent) => cb(ev);
    ipcRenderer.on(EV.menuPane, h);
    return () => ipcRenderer.removeListener(EV.menuPane, h);
  },
  // onZoomKey fires when the content-zoom chord was pressed while a live view
  // owned OS keyboard focus. The wasm zoom owner applies and persists it.
  onZoomKey(cb: (ev: ZoomKeyEvent) => void): () => void {
    const h = (_e: unknown, ev: ZoomKeyEvent) => cb(ev);
    ipcRenderer.on(EV.zoomKey, h);
    return () => ipcRenderer.removeListener(EV.zoomKey, h);
  },
  // onError fires for every main-process failure that must reach the user, from
  // the webview lifecycle and from sidecar boot and exit. It is the one channel
  // the wasm client feeds into its error surface.
  onError(cb: (ev: ErrorEvent) => void): () => void {
    const h = (_e: unknown, ev: ErrorEvent) => cb(ev);
    ipcRenderer.on(EV.error, h);
    return () => ipcRenderer.removeListener(EV.error, h);
  },
};

contextBridge.exposeInMainWorld('gridwell', api);
