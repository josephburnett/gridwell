// What the native layer says in the trace. webviews.ts and window.ts are glue
// over Electron events, and a record built inline there is a record no test
// reads, so every one of them is built here. trace.ts owns the line.

import type { Bounds } from './ipc';
import type { TraceEvent } from './trace';

// One src per owner, as the node's emitters do: the dump is read by grepping
// these.
const VIEW = 'webviews';
const WINDOW = 'window';

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

export function viewBounds(paneId: string, tileId: string, b: Bounds): TraceEvent {
  return view('bounds', paneId, tileId, {
    x: String(b.x),
    y: String(b.y),
    w: String(b.width),
    h: String(b.height),
  });
}

// Hidden is parked off screen rather than destroyed, so the two read as one
// pair of kinds and a dump shows which face the pane was wearing.
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
// carries, so the trace and the strip cannot disagree.
export function viewFailed(paneId: string, tileId: string, message: string): TraceEvent {
  return view('fail', paneId, message, { tile: tileId });
}

export function windowFocused(focused: boolean): TraceEvent {
  return { src: WINDOW, kind: focused ? 'focus' : 'blur', msg: 'root window' };
}

export function windowResized(width: number, height: number): TraceEvent {
  return { src: WINDOW, kind: 'resize', msg: `${width}x${height}` };
}
