import type { Bounds } from './ipc';

// SESSION_PARTITION is the fallback Electron partition for url tiles with no
// owning plugin (a bare id). The real session boundary is per-plugin —
// partitionFor(pluginUuid) — so each plugin's url tiles share one durable
// cookie jar / DOM-storage area (logins, drafts) isolated from other plugins.
// The `persist:` prefix makes it durable on disk across app restarts.
export const SESSION_PARTITION = 'persist:gridwell';

// partitionFor returns the durable partition for a plugin's session: each
// plugin uuid gets its own (persist:plugin-<uuid>). The plugin is the session
// boundary. An empty uuid falls back to the shared partition.
export function partitionFor(pluginUuid: string): string {
  return pluginUuid ? `persist:plugin-${pluginUuid}` : SESSION_PARTITION;
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
