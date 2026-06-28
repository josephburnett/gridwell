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

// sanitizeUserAgent strips the two tokens that mark Chromium's default UA as a
// non-browser embedding — `Electron/<ver>` and the app's own `<AppName>/<ver>` —
// leaving the genuine `Chrome/<ver>` token (and everything else) intact. A live
// url tile IS real Chromium, so the honest fix is to drop the embedding tokens
// rather than fake a different engine: sites that gate on an unknown/outdated
// browser (Slack's "Electron/" check) then see a plain Chrome string. Applied
// once as app.userAgentFallback (the default for every partition and view), so
// it covers all plugins' url tiles. Idempotent: re-running removes nothing.
export function sanitizeUserAgent(ua: string, appName: string): string {
  let out = ua.replace(/\sElectron\/\S+/g, '');
  if (appName) {
    const esc = appName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    out = out.replace(new RegExp(`\\s${esc}\\/\\S+`, 'g'), '');
  }
  return out.replace(/\s+/g, ' ').trim();
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

// RIGHT_DRAG_THRESHOLD is how far (CSS px) the cursor must move with the right
// button held before a press over a live URL view becomes a Gridwell pane
// gesture rather than a plain right-click. Mirrors the canvas dragThreshold
// (client/wasm/main.go) so the live-view and canvas feel identical.
export const RIGHT_DRAG_THRESHOLD = 4;

// dragExceeded reports whether a pointer that started at the press point has
// moved far enough to count as a drag (not a click). Used to tell a right-click
// — which must reach the page's own context menu — apart from a right-drag,
// which arms a pane gesture. dx/dy are the cursor's displacement from the press.
export function dragExceeded(dx: number, dy: number, threshold: number): boolean {
  return dx * dx + dy * dy > threshold * threshold;
}

// controlVisible decides whether a live URL view's corner control (the
// back/ascend circle) should be on screen. The control is the one piece of
// pane chrome that a focused pane shows and an unfocused pane must not — it
// mirrors the canvas rule "per-pane controls belong to the active pane only"
// (render.go drawPane), which a canvas-drawn circle can't enforce here because
// the native view paints on top of the canvas. So: visible only on the focused
// pane, and never while the whole view is parked for a gesture (hidden).
export function controlVisible(hidden: boolean, focused: boolean): boolean {
  return !hidden && focused;
}
