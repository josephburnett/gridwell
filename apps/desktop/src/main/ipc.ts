// The renderer-to-main IPC contract for native url tiles. Main and the preload
// bridge both import it, so the two sides cannot drift. `bounds` is
// panebox.ContentBox's rect in CSS px, which setBounds takes as DIP 1:1.

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
  showMenu: 'gw:showMenu', // PaneRef → void. The bar circle's right-click
                           // over a live url, which works even when the page
                           // hijacks contextmenu.
  choiceMenu: 'gw:choiceMenu', // ChoiceMenuArgs → string | null (the chosen id,
                               // null if dismissed). The renderer declares the
                               // rows; main knows nothing of what they mean.
} as const;

// Live url view's preload → main (send, fire-and-forget). The view swallows the
// renderer's own mouse events, so its preload forwards presses here.
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
  error: 'gw:error', // ErrorEvent — see ErrorEvent below
  openBelow: 'gw:open-below', // OpenBelowEvent — a live view's new-window or ctrl-click link
  freezeUrl: 'gw:freeze-url', // FreezeURLEvent — the context menu's explicit freeze gesture
  menuPane: 'gw:menu-pane', // ContextMenuEvent — a live view's context menu is opening on this pane
  zoomKey: 'gw:zoom-key', // ZoomKeyEvent — the content-zoom chord pressed while a live view owns focus
} as const;

// Physical screen coordinates, independent of the page's zoomFactor.
export interface ViewRightdown {
  sx: number;
  sy: number;
}

// One step of a single-finger drag, which main injects as a mouseWheel. dx/dy
// are the movement since the last step.
export interface ViewTouchScroll {
  sx: number;
  sy: number;
  dx: number;
  dy: number;
}

// Window-content coordinates, which equal the renderer's canvas pixels 1:1.
export interface ForwardedRightdown {
  x: number;
  y: number;
}

export interface PaneRef {
  paneId: string;
}

// One choice in a menu the renderer declared. client/circlemenu owns the rows;
// nothing here reads id or label for a decision. `checked` is the row's state,
// and a row that names no state — an action, like the trace dump — declares
// none, so the menu can draw it as an action instead of an unchecked radio.
export interface ChoiceItem {
  id: string;
  label: string;
  checked?: boolean;
}

export interface ChoiceMenuArgs {
  items: ChoiceItem[];
}

export interface PlaceArgs {
  paneId: string;
  tileId: string;
  // The renderer's per-frame gesture-hide verdict at placement, so a view
  // placed under an open palette starts parked. The registry must not infer it
  // from whichever setHidden arrived last.
  hidden?: boolean;
  // The renderer owns this because a pane goes live on paths that have nothing
  // to do with focus, such as a workspace restore, and main cannot tell those
  // from a descent.
  focused?: boolean;
  url: string;
  bounds: Bounds;
  // The tile's persisted user content zoom; absent means 1.0.
  contentZoom?: number;
  // The persisted back-stack; when valid the view restores it.
  history?: string;
  // Whether the tile survives ascent, which gates Freeze Page.
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
  // Feeds the steal guard: only a focused pane's view may hold OS focus.
  focused: boolean;
}

export interface RemoveArgs {
  paneId: string;
}

// What the renderer persists as the tile's frozen face.
export interface FreezeResult {
  // Empty if the capture failed.
  jpegBase64: string;
  url: string;
  title: string;
  // viewutil.serializeHistory's output, or '' when not captured.
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

// The renderer splits the pane and opens the link as an ephemeral visit.
export interface OpenBelowEvent {
  paneId: string;
  url: string;
}

// The renderer tears the view down with the usual freeze writeback and
// persists the standing frozen intent.
export interface FreezeURLEvent {
  paneId: string;
}

// A context menu is about to open on this pane, so the renderer moves focus
// there first. The native view swallows the right-press until it becomes a
// drag, so this is the only word of a plain one.
export interface ContextMenuEvent {
  paneId: string;
}

// The content-zoom chord pressed while a live url view owns OS keyboard focus,
// relayed so applyContentZoom runs as if it had been typed on the canvas.
export interface ZoomKeyEvent {
  paneId: string;
  key: string;
}

// How loudly a notice presents, decided where the report is made and read by
// client/wasm/webview_bridge.go onto client/errsurface.Severity, which owns
// what each one means. This is the wire spelling, and its one owner.
export type NoticeSeverity = 'error' | 'info';

// The one wire every main-process notice reaches the user through. `source` is
// the key errsurface groups notices by; `message` is shown verbatim.
export interface ErrorEvent {
  source: string;
  message: string;
  severity: NoticeSeverity;
}
