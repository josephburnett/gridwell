// What the native layer says in the trace. webviews.ts and window.ts are glue
// over Electron events, and a record built inline there is a record no test
// reads, so every one of them is built here. trace.ts owns the line.

import type { Bounds } from './ipc';
import type { TraceEvent } from './trace';

// One src per owner, as the node's emitters do: the dump is read by grepping
// these.
const VIEW = 'webviews';
const WINDOW = 'window';
const MENU = 'contextmenu';

// kv names the pane, because the registry keys everything by it and a dump is
// read one pane at a time. msg carries what the pane was showing, which no kv
// repeats.
function view(kind: string, paneId: string, msg: string, extra?: Record<string, string>): TraceEvent {
  return { src: VIEW, kind, msg, kv: { view: paneId, ...extra } };
}

export function viewCreated(paneId: string, tileId: string, url: string): TraceEvent {
  return view('create', paneId, url, { tile: tileId });
}

export function viewDestroyed(paneId: string, tileId: string, url: string): TraceEvent {
  return view('destroy', paneId, url, { tile: tileId });
}

// A view whose webContents ended outside remove(), such as a page that closed
// itself.
export function viewGone(paneId: string, tileId: string, url: string): TraceEvent {
  return view('gone', paneId, url, { tile: tileId });
}

// A view handed to another pane with its page, which is neither a create nor
// a destroy: a dump reading one pane follows it by kv.from.
export function viewMoved(fromPaneId: string, toPaneId: string, tileId: string): TraceEvent {
  return view('move', toPaneId, tileId, { from: fromPaneId });
}

export function viewBounds(paneId: string, tileId: string, b: Bounds): TraceEvent {
  return view('bounds', paneId, tileId, {
    x: String(b.x),
    y: String(b.y),
    w: String(b.width),
    h: String(b.height),
  });
}

// Hidden is parked off screen rather than destroyed, so the two read as one
// pair of kinds and a dump shows which preview the pane was wearing.
export function viewShown(paneId: string, tileId: string, hidden: boolean): TraceEvent {
  return view(hidden ? 'hide' : 'show', paneId, tileId);
}

export function viewFocused(paneId: string, tileId: string, focused: boolean): TraceEvent {
  return view(focused ? 'focus' : 'blur', paneId, tileId);
}

// A navigation is two records, because the gap between them is where a page
// hangs and the pane sits blank.
export function viewNav(paneId: string, tileId: string, done: boolean, url: string): TraceEvent {
  return view(done ? 'nav-done' : 'nav-start', paneId, url, { tile: tileId });
}

// message is viewutil.failLoadMessage, the same sentence the user's notice
// carries, so the trace and the notice row cannot disagree.
export function viewFailed(paneId: string, tileId: string, message: string): TraceEvent {
  return view('fail', paneId, message, { tile: tileId });
}

// Both native menus, because a menu that popped and a row that ran are two
// different stories when a click seems to have done nothing. A menu over a
// live view names its pane; the circle's declared choices have none.
export function menuOpened(menu: string, paneId?: string): TraceEvent {
  return { src: MENU, kind: 'open', msg: menu, ...(paneId ? { kv: { view: paneId } } : {}) };
}

export function menuChose(menu: string, row: string, paneId?: string): TraceEvent {
  return { src: MENU, kind: 'choose', msg: row, kv: { menu, ...(paneId ? { view: paneId } : {}) } };
}

export function windowFocused(focused: boolean): TraceEvent {
  return { src: WINDOW, kind: focused ? 'focus' : 'blur', msg: 'root window' };
}

// What the window manager has made of the root window, read by window.ts on
// creation and on each Electron window event. It is the one window-geometry
// record: a window the user cannot resize or snap is diagnosed from these
// fields on a platform this repo's gates do not run.
interface WindowState {
  resizable: boolean;
  maximized: boolean;
  fullScreen: boolean;
  minimizable: boolean;
  maximizable: boolean;
  bounds: Bounds;
  workArea: { width: number; height: number };
  scaleFactor: number;
  platform: string;
  electron: string;
  // [0, 0] is Electron's unset, and is left out of the record.
  minSize: [number, number];
  maxSize: [number, number];
}

// A drag fires resize and move per frame, which would wash the ring, so those
// are recorded once they settle; every other cause is recorded as it fires.
export const WINDOW_SETTLE_MS = 250;

export function windowStateRecorder(record: (cause: string) => void): {
  now: (cause: string) => void;
  settle: (cause: string) => void;
  stop: () => void;
} {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return {
    now: record,
    settle: (cause) => {
      clearTimeout(timer);
      timer = setTimeout(() => record(cause), WINDOW_SETTLE_MS);
    },
    stop: () => clearTimeout(timer),
  };
}

// msg names the event that caused the read ('create', 'resize', 'maximize',
// ...), so a dump greps one kind and reads the cause beside it.
export function windowState(cause: string, s: WindowState): TraceEvent {
  const kv: Record<string, string> = {
    resizable: String(s.resizable),
    maximized: String(s.maximized),
    fullScreen: String(s.fullScreen),
    minimizable: String(s.minimizable),
    maximizable: String(s.maximizable),
    x: String(s.bounds.x),
    y: String(s.bounds.y),
    w: String(s.bounds.width),
    h: String(s.bounds.height),
    workW: String(s.workArea.width),
    workH: String(s.workArea.height),
    scale: String(s.scaleFactor),
    platform: s.platform,
    electron: s.electron,
  };
  if (s.minSize[0] || s.minSize[1]) kv.minSize = `${s.minSize[0]}x${s.minSize[1]}`;
  if (s.maxSize[0] || s.maxSize[1]) kv.maxSize = `${s.maxSize[0]}x${s.maxSize[1]}`;
  return { src: WINDOW, kind: 'state', msg: cause, kv };
}
