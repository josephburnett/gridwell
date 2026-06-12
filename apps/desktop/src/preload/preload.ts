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
  RemoveArgs,
  PaneRef,
  FreezeResult,
  FrameEvent,
  NavEvent,
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
  removeWebview(args: RemoveArgs): Promise<FreezeResult> {
    return ipcRenderer.invoke(CH.remove, args);
  },
  goBack(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.goBack, args);
  },
  reload(args: PaneRef): Promise<void> {
    return ipcRenderer.invoke(CH.reload, args);
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
};

export type GridwellBridge = typeof api;

contextBridge.exposeInMainWorld('gridwell', api);
