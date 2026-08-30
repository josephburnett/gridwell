// Preload bridge: the renderer's only door to the native URL-tile machinery
// in main. Exposed under window.gridwell; the WASM client calls these
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
  // The host's OWN capability declaration (2026-08-13): which bridge
  // halves this preload implements. caps.Derive reads it — a host
  // exposing the bridge no longer implies every native feature. Since
  // 2026-08-29 there is one half left: shells ride the web door, so no
  // host implements anything for them.
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
  // showMenu pops the live view's context menu (Freeze Page included) with
  // no in-page context — the bar circle's right-click, which stays reachable
  // when the page itself hijacks contextmenu.
  showMenu(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.showMenu, args);
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
  // onFreezeURL fires when the user picked "Freeze Page" in a live view's
  // context menu (issue #237); the wasm freezes the pane's view and stores
  // the standing intent.
  onFreezeURL(cb: (ev: FreezeURLEvent) => void): () => void {
    const h = (_e: unknown, ev: FreezeURLEvent) => cb(ev);
    ipcRenderer.on(EV.freezeUrl, h);
    return () => ipcRenderer.removeListener(EV.freezeUrl, h);
  },
  // onZoomKey fires when the content-zoom chord was pressed while a live
  // view owned OS keyboard focus (issue #170); the wasm zoom owner applies
  // and persists it.
  onZoomKey(cb: (ev: ZoomKeyEvent) => void): () => void {
    const h = (_e: unknown, ev: ZoomKeyEvent) => cb(ev);
    ipcRenderer.on(EV.zoomKey, h);
    return () => ipcRenderer.removeListener(EV.zoomKey, h);
  },
  // onError fires for every main-process failure that must reach the user
  // (webview lifecycle, sidecar boot/exit) — the one channel the wasm
  // client feeds into its error surface.
  onError(cb: (ev: ErrorEvent) => void): () => void {
    const h = (_e: unknown, ev: ErrorEvent) => cb(ev);
    ipcRenderer.on(EV.error, h);
    return () => ipcRenderer.removeListener(EV.error, h);
  },
};

contextBridge.exposeInMainWorld('gridwell', api);
