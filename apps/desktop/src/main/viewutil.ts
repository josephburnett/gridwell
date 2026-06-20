import type { Bounds } from './ipc';

// SESSION_PARTITION is the single Electron session partition shared by ALL
// live URL tiles. Tiles behave like browser tabs: one cookie jar and one
// DOM-storage (localStorage) area for every tile, so a login — or an
// autosaved, unsubmitted comment draft — made in one tile is visible in
// every other tile and survives freeze/live cycles. The `persist:` prefix
// makes it durable on disk, so it also survives app restarts.
//
// This is a deliberate change from the older per-tile partition
// (`persist:tile-<objectId>`): the user wants shared sign-in across tiles,
// not isolation. There is one owner of the partition name (here) so the
// flush/clear helpers can't drift out of sync with the view creator.
export const SESSION_PARTITION = 'persist:gridwell';

// roundBounds snaps a CSS-pixel rect to integer DIP for setBounds, clamping
// width/height to a 1px floor so a collapsed pane never asks for a 0-sized
// view (which some platforms reject).
export function roundBounds(b: Bounds): Bounds {
  return {
    x: Math.round(b.x),
    y: Math.round(b.y),
    width: Math.max(1, Math.round(b.width)),
    height: Math.max(1, Math.round(b.height)),
  };
}

// boundsEqual reports whether two (already-rounded) rects match, so the
// registry can skip redundant setBounds churn on every render frame.
export function boundsEqual(a: Bounds, b: Bounds): boolean {
  return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height;
}
