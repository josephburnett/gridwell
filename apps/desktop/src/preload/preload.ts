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
  ChoiceMenuArgs,
  RemoveArgs,
  PaneRef,
  FreezeResult,
} from '../main/ipc';

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

  ...listeners,
};

contextBridge.exposeInMainWorld('gridwell', api);
