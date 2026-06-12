import type { Bounds } from './ipc';

// partitionFor derives the Electron session partition name for a tile. A
// `persist:` prefix makes the session durable on disk (cookies, storage,
// logins survive restarts). Keyed by the tile's stable objectId so the
// same tile shares one cookie jar across panes and freeze/live cycles,
// while distinct tiles stay isolated (Gmail in tile A ≠ tile B).
export function partitionFor(objectId: string): string {
  // objectId is a UUID from the store; sanitize defensively so a stray
  // character can't change the partition's meaning.
  const safe = objectId.replace(/[^A-Za-z0-9_-]/g, '');
  return `persist:tile-${safe}`;
}

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
