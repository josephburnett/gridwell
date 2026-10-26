// The renderer's only way to the native url-tile machinery in main, exposed as
// window.gridwell and called by client/wasm/webview_bridge.go. ipc.ts owns the
// channels and the payload shapes.
import { contextBridge, ipcRenderer } from 'electron';
import { CH, EV } from '../main/ipc';

// The main-to-renderer pushes, listener name → channel. EV in ipc.ts says what
// each one carries; nothing here reads a payload, because only
// client/wasm/webview_bridge.go does, through js.Value. This table is the one
// owner of the listener half of the bridge vocabulary, which
// webview-vocabulary.test.ts pins against the wasm's string literals.
const LISTENERS = {
  onFrame: EV.frame,
  onNav: EV.nav,
  onRightForward: EV.rightForward,
  onMiddleForward: EV.middleForward,
  onLeftForward: EV.leftForward,
  onOpenBelow: EV.openBelow,
  onFreezeURL: EV.freezeUrl,
  onContextMenu: EV.menuPane,
  onZoomKey: EV.zoomKey,
  onEscapeKey: EV.escapeKey,
  onViewGone: EV.viewGone,
  onRouteMove: EV.routeMove,
  onError: EV.error,
} as const;

// A listener takes the callback and returns its unsubscribe.
type Subscribe = (cb: (ev: unknown) => void) => () => void;

function subscriber(channel: string): Subscribe {
  return (cb) => {
    const h = (_e: unknown, ev: unknown) => cb(ev);
    ipcRenderer.on(channel, h);
    return () => ipcRenderer.removeListener(channel, h);
  };
}

const listeners = Object.fromEntries(
  Object.entries(LISTENERS).map(([name, channel]) => [name, subscriber(channel)]),
) as Record<keyof typeof LISTENERS, Subscribe>;

// The renderer-to-main verbs, method name → channel, each invoked with the
// one payload ipc.ts declares for its channel. Like LISTENERS, this table is
// the one owner of its half of the bridge vocabulary, which
// webview-vocabulary.test.ts pins against the wasm's string literals.
const VERBS = {
  placeWebview: CH.place,
  setBounds: CH.setBounds,
  setHidden: CH.setHidden,
  // The tile's persisted content_zoom; main composes it with the min-width zoom.
  setZoom: CH.setZoom,
  // Hands a live view to another pane without a reload.
  moveWebview: CH.move,
  removeWebview: CH.remove,
  goBack: CH.goBack,
  reload: CH.reload,
  // The bar circle's right-click over a live url, with no in-page context.
  showMenu: CH.showMenu,
  // A native menu of the choices the renderer declares, answering with the
  // chosen id or null. The renderer owns what the rows mean.
  showChoiceMenu: CH.choiceMenu,
  // Which live panes main's mirror pump captures; see MirroredArgs.
  setMirrored: CH.setMirrored,
  // Main's own trace half, handed to the node before a dump; see
  // TraceClient.handOver. Its args are bridgeVerb's and carry nothing.
  handOverTrace: CH.handOverTrace,
  // Hands an http(s) address to the default browser; anything else rejects.
  openOutside: CH.openOutside,
  // The handshake's routes and declarations, which main routes a live page's
  // navigations by before the page moves.
  setRoutes: CH.setRoutes,
} as const;

const verbs = Object.fromEntries(
  Object.entries(VERBS).map(([name, channel]) => [name, (args: unknown) => ipcRenderer.invoke(channel, args)]),
) as Record<keyof typeof VERBS, (args: unknown) => Promise<unknown>>;

const api = {
  version: 1,
  // Which parts of the bridge this preload implements. caps.Derive reads it, so
  // exposing the bridge does not imply every native feature.
  caps: { liveUrl: true, choiceMenu: true, trace: true },

  ...verbs,
  ...listeners,
};

contextBridge.exposeInMainWorld('gridwell', api);
