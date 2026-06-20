// The renderer↔main IPC contract for native URL tiles. Both the main
// process (ipcMain handlers) and the preload bridge import this module for
// the channel names and payload types, so the two sides can't drift.
//
// Geometry: `bounds` is the URL tile's content box in CSS pixels, relative
// to the window's content area — exactly what the WASM canvas computes via
// panebox.ContentBox. Electron's WebContentsView.setBounds takes DIP, which
// equals CSS px in the renderer's coordinate space, so the mapping is 1:1
// (DPR scaling of page content is handled inside the view).

export interface Bounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

// Renderer → main (invoke/handle, awaitable).
export const CH = {
  place: 'gw:place',       // PlaceArgs → void
  setBounds: 'gw:setBounds', // SetBoundsArgs → void
  setHidden: 'gw:setHidden', // SetHiddenArgs → void
  remove: 'gw:remove',     // RemoveArgs → FreezeResult
  goBack: 'gw:goBack',     // PaneRef → void
  reload: 'gw:reload',     // PaneRef → void
} as const;

// Control overlay (the corner-button view) → main (send, fire-and-forget).
// Payload is the mouse button (0 = left → back, else → ascend).
export const CTRL = {
  click: 'gw:control-click',
} as const;

// Live URL view's injected preload → main (send, fire-and-forget). The view
// swallows the renderer's own mouse events, so its preload forwards a
// right-button press here; the renderer then begins a pane gesture over the
// live page (mirroring the shell overlay). Left button / wheel / keys stay
// with the page — native browsing is untouched.
export const VIEW = {
  rightdown: 'gw:view-rightdown',   // ViewRightdown
  middledown: 'gw:view-middledown', // ViewRightdown (same payload: screen coords)
} as const;

// Main → renderer (send, fire-and-forget).
export const EV = {
  frame: 'gw:frame', // FrameEvent
  nav: 'gw:nav',     // NavEvent
  controlAscend: 'gw:control-ascend', // PaneRef — corner button right-clicked
  rightForward: 'gw:right-forward',   // ForwardedRightdown — over a live URL view
  middleForward: 'gw:middle-forward', // ForwardedRightdown — middle-click over a live URL view (ascend)
} as const;

// ViewRightdown carries the press in physical screen coordinates
// (MouseEvent.screenX/screenY), which are independent of the page's
// zoomFactor; main converts them to window-content coords via getContentBounds.
export interface ViewRightdown {
  sx: number;
  sy: number;
}

// ForwardedRightdown carries the press in window-content coordinates, which
// equal the renderer's canvas pixels (1:1, DIP == CSS px) — ready to feed
// straight into the canvas gesture pipeline.
export interface ForwardedRightdown {
  x: number;
  y: number;
}

export interface PaneRef {
  paneId: string;
}

export interface PlaceArgs {
  paneId: string;
  tileId: number;
  // objectId identifies which tile a pane is showing, so a pane reused for a
  // different tile tears its old view down instead of just re-navigating.
  // Session state is NOT keyed by it: all tiles share one persistent session
  // (see SESSION_PARTITION) so logins/drafts are shared like browser tabs.
  objectId: string;
  url: string;
  bounds: Bounds;
}

export interface SetBoundsArgs {
  paneId: string;
  bounds: Bounds;
}

export interface SetHiddenArgs {
  paneId: string;
  hidden: boolean;
}

export interface RemoveArgs {
  paneId: string;
}

// FreezeResult is returned by `remove`: the final capture + the page's last
// URL/title, so the renderer can persist the frozen preview via SetURLState.
export interface FreezeResult {
  // JPEG bytes of the final frame, base64-encoded (empty if capture failed).
  jpegBase64: string;
  url: string;
  title: string;
}

export interface FrameEvent {
  paneId: string;
  tileId: number;
  jpegBase64: string;
}

export interface NavEvent {
  paneId: string;
  tileId: number;
  url: string;
  title: string;
}
