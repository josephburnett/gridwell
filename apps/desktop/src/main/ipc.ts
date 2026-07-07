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
  setZoom: 'gw:setZoom', // SetZoomArgs → void (user content zoom, issue #82)
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
// swallows the renderer's own mouse events, so its preload forwards button
// presses here. Left button (focus intent only — the click still reaches the
// page, no preventDefault) transfers pane focus. Right/middle are gesture paths.
export const VIEW = {
  rightdown: 'gw:view-rightdown',   // ViewRightdown
  middledown: 'gw:view-middledown', // ViewRightdown (same payload: screen coords)
  leftdown: 'gw:view-leftdown',     // ViewRightdown (focus intent; no preventDefault in preload)
} as const;

// Main → renderer (send, fire-and-forget).
export const EV = {
  frame: 'gw:frame', // FrameEvent
  nav: 'gw:nav',     // NavEvent
  controlAscend: 'gw:control-ascend', // PaneRef — corner button right-clicked
  rightForward: 'gw:right-forward',   // ForwardedRightdown — over a live URL view
  middleForward: 'gw:middle-forward', // ForwardedRightdown — middle-click over a live URL view (ascend)
  leftForward: 'gw:left-forward',     // ForwardedRightdown — left-down over a live URL view (focus intent)
  error: 'gw:error', // ErrorEvent — the ONE wire for every main-process failure
                      // (webview, session, sidecar) that must reach the user.
                      // Charter §1/§6: one owner, no second "silent" path for
                      // a main-process failure.
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
  objectId: string;
  url: string;
  bounds: Bounds;
  // pluginUuid is the uuid of the plugin that owns the tile — the session
  // boundary. It selects the Electron partition (persist:plugin-<uuid>) so url
  // tiles in different plugins get isolated cookie jars / web storage. Empty
  // falls back to the shared partition.
  pluginUuid: string;
  // proxyEndpoint is the grid-stamped network context for this tile's plugin
  // ("socks5://host:port" through a node mount; "" = direct).
  proxyEndpoint?: string;
  // contentZoom is the tile's persisted USER content zoom (issue #82),
  // composed with the min-width layout zoom. 0/absent = 1.0.
  contentZoom?: number;
  // history is the tile's persisted navigation back-stack; when valid the
  // view restores it instead of a bare loadURL (issue #113).
  history?: string;
}

export interface SetZoomArgs {
  paneId: string;
  zoom: number;
}

export interface SetBoundsArgs {
  paneId: string;
  bounds: Bounds;
}

export interface SetHiddenArgs {
  paneId: string;
  hidden: boolean;
  // focused is whether this pane is the focused pane. Drives the corner
  // control's visibility independently of `hidden`: an unfocused live pane
  // keeps its web content on screen but hides its corner circle, so exactly
  // one pane shows the control at a time (see webviews.ts controlVisible).
  focused: boolean;
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
  // history is the serialized navigation back-stack (viewutil.serializeHistory)
  // so a revived tile can still go back (issue #113). '' when not captured.
  history: string;
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

// ErrorEvent is the one payload shape for EV.error: every main-process
// failure site (webview lifecycle, session hydrate/dehydrate, sidecar boot/
// exit) reports through this same wire, never a bespoke one. `source` is a
// stable key the wasm errsurface groups notices by (one row per source — see
// client/errsurface): 'electron:webview' | 'electron:session' |
// 'electron:backend'. `message` is the human-readable text shown verbatim.
export interface ErrorEvent {
  source: string;
  message: string;
}
