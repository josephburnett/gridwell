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

  // Shell transport (2026-07-26: the WS bridge is gone; PTY bytes ride a
  // main-process gRPC OpenShell stream, relayed here per pane).
  shellOpen: 'gw:shell-open',     // ShellOpenArgs → void
  shellWrite: 'gw:shell-write',   // ShellWriteArgs → void
  shellResize: 'gw:shell-resize', // ShellResizeArgs → void
  shellClose: 'gw:shell-close',   // PaneRef → void
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
  rightForward: 'gw:right-forward',   // ForwardedRightdown — over a live URL view
  middleForward: 'gw:middle-forward', // ForwardedRightdown — middle-click over a live URL view (ascend)
  leftForward: 'gw:left-forward',     // ForwardedRightdown — left-down over a live URL view (focus intent)
  shellData: 'gw:shell-data', // ShellDataEvent — PTY output for a pane's terminal
  shellExit: 'gw:shell-exit', // ShellExitEvent — the pane's stream ended (exactly once)
  error: 'gw:error', // ErrorEvent — the ONE wire for every main-process failure
  openBelow: 'gw:open-below', // OpenBelowEvent — a live view's new-window/ctrl-click link (issue #111)
  freezeUrl: 'gw:freeze-url', // FreezeURLEvent — the context menu's explicit freeze gesture (issue #237)
  zoomKey: 'gw:zoom-key', // ZoomKeyEvent — the content-zoom chord pressed while a live view owns focus (issue #170)
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

// Shell transport payloads. Data crosses as Uint8Array both ways (Electron's
// structured clone carries it natively — no base64 hop for a keystroke).
export interface ShellOpenArgs {
  paneId: string;
  // tileId is the globally-qualified "<plugin-uuid>/<id>" of the shell tile
  // that OWNS the PTY session (the renderer resolves a link to its target
  // before opening — the session is keyed by the owner).
  tileId: string;
  cols: number;
  rows: number;
}
export interface ShellWriteArgs {
  paneId: string;
  data: Uint8Array;
}
export interface ShellResizeArgs {
  paneId: string;
  cols: number;
  rows: number;
}
export interface ShellDataEvent {
  paneId: string;
  data: Uint8Array;
}
export interface ShellExitEvent {
  paneId: string;
  // message is '' for a clean end (local close, remote hangup), else the
  // transport/status error text the renderer surfaces (charter §6).
  message: string;
  // sessionGone marks the server's "this tmux session no longer exists"
  // verdict — the renderer flips the refresh affordance off.
  sessionGone: boolean;
}

export interface PlaceArgs {
  paneId: string;
  tileId: number;
  // objectId identifies which tile a pane is showing, so a pane reused for a
  // different tile tears its old view down instead of just re-navigating.
  objectId: string;
  url: string;
  bounds: Bounds;
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
  tileId: number;
  jpegBase64: string;
}

export interface NavEvent {
  paneId: string;
  tileId: number;
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
// failure site (webview lifecycle, shell stream, sidecar boot/
// exit) reports through this same wire, never a bespoke one. `source` is a
// stable key the wasm errsurface groups notices by (one row per source — see
// client/errsurface): 'electron:webview' | 'electron:shell' |
// 'electron:backend'. `message` is the human-readable text shown verbatim.
export interface ErrorEvent {
  source: string;
  message: string;
}
