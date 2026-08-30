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
  showMenu: 'gw:showMenu', // PaneRef → void — pop the live view's context
                           // menu with no in-page context: the bar circle's
                           // right-click, reachable even when the page
                           // hijacks contextmenu.
} as const;

// Live URL view's injected preload → main (send, fire-and-forget). The view
// swallows the renderer's own mouse events, so its preload forwards button
// presses here. Left button (focus intent only — the click still reaches the
// page, no preventDefault) transfers pane focus. Right/middle are gesture paths.
export const VIEW = {
  rightdown: 'gw:view-rightdown',   // ViewRightdown
  middledown: 'gw:view-middledown', // ViewRightdown (same payload: screen coords)
  leftdown: 'gw:view-leftdown',     // ViewRightdown (focus intent; no preventDefault in preload)
  touchscroll: 'gw:view-touchscroll', // ViewTouchScroll — single-finger drag over live content
} as const;

// Main → renderer (send, fire-and-forget).
export const EV = {
  frame: 'gw:frame', // FrameEvent
  nav: 'gw:nav',     // NavEvent
  rightForward: 'gw:right-forward',   // ForwardedRightdown — over a live URL view
  middleForward: 'gw:middle-forward', // ForwardedRightdown — middle-click over a live URL view (ascend)
  leftForward: 'gw:left-forward',     // ForwardedRightdown — left-down over a live URL view (focus intent)
  error: 'gw:error', // ErrorEvent — the ONE wire for every main-process failure
                     // (electron:webview | electron:backend)
                     // that must reach the user. Charter §6: one owner, no
                     // second "silent" path for a main-process failure.
  openBelow: 'gw:open-below', // OpenBelowEvent — a live view's new-window/ctrl-click link (issue #111)
  freezeUrl: 'gw:freeze-url', // FreezeURLEvent — the context menu's explicit freeze gesture (issue #237)
  zoomKey: 'gw:zoom-key', // ZoomKeyEvent — the content-zoom chord pressed while a live view owns focus (issue #170)
} as const;

// ViewRightdown carries the press in physical screen coordinates
// (MouseEvent.screenX/screenY), which are independent of the page's
// zoomFactor; main converts them to window-content coords via getContentBounds.
export interface ViewRightdown {
  sx: number;
  sy: number;
}

// ViewTouchScroll is one movement step of a single-finger drag over live web
// content. Chromium does not turn raw touches into scroll gestures inside an
// embedded WebContentsView, so the view's preload forwards the finger's
// per-move delta here; main injects an equivalent mouseWheel into the SAME
// view at the finger's position, scrolling whatever scrollable element sits
// under it. sx/sy are physical screen px (like ViewRightdown); dx/dy are the
// finger's movement since the previous step, same units.
export interface ViewTouchScroll {
  sx: number;
  sy: number;
  dx: number;
  dy: number;
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
  tileId: string;
  // hidden: the renderer's per-frame gesture-hide verdict at the moment of
  // placement (liveOverlaysHidden) — a view placed during a drag or under
  // an open palette starts parked. The renderer owns this fact; the
  // registry used to infer it from the LAST setHidden it happened to
  // receive, which ranged over a Go map (2026-08-27).
  hidden?: boolean;
  url: string;
  bounds: Bounds;
  // contentZoom is the tile's persisted USER content zoom (issue #82),
  // composed with the min-width layout zoom. 0/absent = 1.0.
  contentZoom?: number;
  // history is the tile's persisted navigation back-stack; when valid the
  // view restores it instead of a bare loadURL (issue #113).
  history?: string;
  // durable is whether the tile survives ascent (false = ephemeral visit);
  // gates the context menu's Freeze Page (issue #240). Absent = false.
  durable?: boolean;
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
  // focused is whether this pane is the focused pane. Feeds the registry's
  // focus-steal guard: only the focused pane's view may hold OS keyboard
  // focus (issue #172).
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
  tileId: string;
  jpegBase64: string;
}

export interface NavEvent {
  paneId: string;
  tileId: string;
  url: string;
  title: string;
}

// OpenBelowEvent carries a link a live view tried to open in a NEW WINDOW
// (target=_blank, window.open, ctrl/cmd-click — everything Chromium routes to
// the window-open handler). The renderer splits the pane and opens the url as
// an ephemeral visit in the lower half (issue #111).
export interface OpenBelowEvent {
  paneId: string;
  url: string;
}

// FreezeURLEvent: the user picked "Freeze Page" in a live view's context
// menu (issue #237). The renderer tears the view down with the usual freeze
// writeback and persists the standing frozen intent on the tile.
export interface FreezeURLEvent {
  paneId: string;
}

// ZoomKeyEvent: Ctrl/Cmd +/=/-/0 pressed while a live URL view owns OS
// keyboard focus (issue #170). Main intercepts it in before-input-event and
// relays it here so the renderer's applyContentZoom — the one owner of the
// cache update + SetContentZoom persistence — runs, exactly as if the chord
// had been typed on the canvas.
export interface ZoomKeyEvent {
  paneId: string;
  key: string;
}

// ErrorEvent is the one payload shape for EV.error: every main-process
// failure site (webview lifecycle, sidecar boot/exit) reports through this
// same wire, never a bespoke one. `source` is a stable key the wasm
// errsurface groups notices by (one row per source — see
// client/errsurface): 'electron:webview' | 'electron:backend'. `message` is the human-readable text shown verbatim.
export interface ErrorEvent {
  source: string;
  message: string;
}
