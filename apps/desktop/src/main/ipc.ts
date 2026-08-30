// The renderer↔main IPC contract for native URL tiles. Both the main
// process (ipcMain handlers) and the preload bridge import this module for
// the channel names and payload types, so the two sides can't drift.
//
// Geometry: `bounds` is the url tile's content box in CSS pixels relative to
// the window's content area, exactly what the wasm canvas computes through
// panebox.ContentBox. WebContentsView.setBounds takes DIP, which equals CSS px
// in the renderer's coordinate space, so the mapping is 1:1. DPR scaling of
// page content happens inside the view.

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
  setZoom: 'gw:setZoom', // SetZoomArgs → void (user content zoom)
  remove: 'gw:remove',     // RemoveArgs → FreezeResult
  goBack: 'gw:goBack',     // PaneRef → void
  showMenu: 'gw:showMenu', // PaneRef → void — pop the live view's context
                           // menu with no in-page context: the bar circle's
                           // right-click, reachable even when the page
                           // hijacks contextmenu.
} as const;

// Live url view's injected preload → main (send, fire-and-forget). The view
// swallows the renderer's own mouse events, so its preload forwards button
// presses here. The left button transfers pane focus and nothing more: the
// click still reaches the page, with no preventDefault. Right and middle are
// gesture paths.
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
  rightForward: 'gw:right-forward',   // ForwardedRightdown — over a live url view
  middleForward: 'gw:middle-forward', // ForwardedRightdown — middle-click over a live url view (ascend)
  leftForward: 'gw:left-forward',     // ForwardedRightdown — left-down over a live url view (focus intent)
  error: 'gw:error', // ErrorEvent — the one wire for every main-process failure
                     // (electron:webview | electron:backend) that must reach
                     // the user. There is no second, silent path.
  openBelow: 'gw:open-below', // OpenBelowEvent — a live view's new-window or ctrl-click link
  freezeUrl: 'gw:freeze-url', // FreezeURLEvent — the context menu's explicit freeze gesture
  zoomKey: 'gw:zoom-key', // ZoomKeyEvent — the content-zoom chord pressed while a live view owns focus
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
// per-move delta here, and main injects an equivalent mouseWheel into the same
// view at the finger's position, scrolling whatever scrollable element sits
// under it. sx/sy are physical screen px, as in ViewRightdown; dx/dy are the
// finger's movement since the previous step, in the same units.
export interface ViewTouchScroll {
  sx: number;
  sy: number;
  dx: number;
  dy: number;
}

// ForwardedRightdown carries the press in window-content coordinates, which
// equal the renderer's canvas pixels 1:1 (DIP == CSS px), ready to feed
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
  // placement (liveOverlaysHidden), so a view placed during a drag or under an
  // open palette starts parked. The renderer owns this fact; the registry must
  // not infer it from whichever setHidden happened to arrive last.
  hidden?: boolean;
  url: string;
  bounds: Bounds;
  // contentZoom is the tile's persisted user content zoom, composed with the
  // min-width layout zoom. Zero or absent means 1.0.
  contentZoom?: number;
  // history is the tile's persisted navigation back-stack; when valid the view
  // restores it instead of a bare loadURL.
  history?: string;
  // durable is whether the tile survives ascent; false is an ephemeral visit.
  // It gates the context menu's Freeze Page. Absent means false.
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
  // focused is whether this pane is the focused pane. It feeds the registry's
  // focus-steal guard: only the focused pane's view may hold OS keyboard focus.
  focused: boolean;
}

export interface RemoveArgs {
  paneId: string;
}

// FreezeResult is returned by `remove`: the final capture plus the page's last
// url and title, which the renderer persists as the tile's frozen face.
export interface FreezeResult {
  // JPEG bytes of the final frame, base64-encoded (empty if capture failed).
  jpegBase64: string;
  url: string;
  title: string;
  // history is the serialized navigation back-stack (viewutil.serializeHistory)
  // so a revived tile can still go back. '' when not captured.
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

// OpenBelowEvent carries a link a live view tried to open in a new window:
// target=_blank, window.open, ctrl or cmd-click, everything Chromium routes to
// the window-open handler. The renderer splits the pane and opens the url as an
// ephemeral visit in the lower half.
export interface OpenBelowEvent {
  paneId: string;
  url: string;
}

// FreezeURLEvent: the user picked "Freeze Page" in a live view's context menu.
// The renderer tears the view down with the usual freeze writeback and persists
// the standing frozen intent on the tile.
export interface FreezeURLEvent {
  paneId: string;
}

// ZoomKeyEvent: Ctrl/Cmd with +, =, - or 0 pressed while a live url view owns
// OS keyboard focus. Main intercepts it in before-input-event and relays it
// here, so the renderer's applyContentZoom runs exactly as if the chord had
// been typed on the canvas. applyContentZoom is the one owner of the cache
// update and the SetContentZoom write.
export interface ZoomKeyEvent {
  paneId: string;
  key: string;
}

// ErrorEvent is the one payload shape for EV.error. Every main-process failure
// site (webview lifecycle, sidecar boot and exit) reports through this wire,
// never a bespoke one. `source` is a stable key the wasm errsurface groups
// notices by, one row per source: 'electron:webview' or 'electron:backend'.
// `message` is shown to the user verbatim.
export interface ErrorEvent {
  source: string;
  message: string;
}
