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

// controlBounds places a corner control (the back/ascend circle) of the given
// size at the bottom-right of a view's content box, inset by margin. Pure
// geometry, extracted from webviews.ts so the corner placement is unit-testable
// (it's native chrome the canvas can't draw, and a wrong rect here puts the
// control off the pane — exactly the kind of bounds bug make check can't see).
export function controlBounds(b: Bounds, size: number, margin: number): Bounds {
  return {
    x: b.x + b.width - size - margin,
    y: b.y + b.height - size - margin,
    width: size,
    height: size,
  };
}

// PARK_COORD is far enough off any display that a parked view/control is not
// visible; the registry parks rather than destroys so the page keeps running.
export const PARK_COORD = -100000;

// parkedBounds is the off-screen rect a view or control is moved to while it is
// "hidden" — during a drag/gesture/modal so canvas overlays can paint where the
// native view sits, or for an unfocused pane's corner control. Width/height are
// preserved (some platforms reject a 0-sized view) so un-parking is a pure move.
export function parkedBounds(width: number, height: number): Bounds {
  return { x: PARK_COORD, y: PARK_COORD, width, height };
}

// minWidthZoomFactor is the page zoom that keeps a narrow live URL view laying
// out at minWidth instead of reflowing to a cramped mobile layout: 1 at or above
// minWidth, else width/minWidth clamped to a 0.25 floor. A native WebContentsView
// can't render wider than its bounds and be clipped, so scaling the page to fit
// is the closest thing to "min width + horizontal scroll". Pure, so the clamp and
// the threshold are pinned by a test rather than only observable in the live app.
export function minWidthZoomFactor(width: number, minWidth: number): number {
  return width >= minWidth ? 1 : Math.max(0.25, width / minWidth);
}

// RIGHT_DRAG_THRESHOLD is how far (CSS px) the cursor must move with the right
// button held before a press over a live URL view becomes a Gridwell pane
// gesture rather than a plain right-click. Mirrors the canvas dragThreshold
// (client/wasm/main.go) so the live-view and canvas feel identical.
export const RIGHT_DRAG_THRESHOLD = 4;

// RIGHT_DRAG_TIME_MS is the minimum duration (ms) that a right-button press must
// be held before a distance-exceeding move counts as a pane gesture. A quick
// trackpad tap that drifts a few pixels past the threshold in under this window
// is still classified as a click — so the native context menu fires. Mirrored
// verbatim in urlview-preload.ts (can't import there; gesture-threshold.test.ts
// drift-lints both copies).
export const RIGHT_DRAG_TIME_MS = 200;

// dragExceeded reports whether a pointer that started at the press point has
// moved far enough to count as a drag (not a click). Used to tell a right-click
// — which must reach the page's own context menu — apart from a right-drag,
// which arms a pane gesture. dx/dy are the cursor's displacement from the press.
export function dragExceeded(dx: number, dy: number, threshold: number): boolean {
  return dx * dx + dy * dy > threshold * threshold;
}

// classifyRightPress returns true (= drag) only when BOTH the distance and time
// thresholds are exceeded. Requiring both means a normal jittery trackpad tap
// (large distance but very short hold) still classifies as a click and produces
// the context menu, while an intentional right-drag (large distance AND the
// button held for a moment) correctly arms the pane gesture. Pure function,
// unit-tested; urlview-preload.ts inlines equivalent logic (can't import).
export function classifyRightPress(
  dx: number,
  dy: number,
  durationMs: number,
  distThreshold: number,
  timeThresholdMs: number,
): boolean {
  return dx * dx + dy * dy > distThreshold * distThreshold && durationMs >= timeThresholdMs;
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

// ERR_ABORTED is Chromium's net error code for a navigation the page/user
// itself cancelled (a redirect superseded by another navigation, window.stop(),
// a same-document replace) — not a genuine failure. did-fail-load fires for
// this constantly during ordinary navigation, so it must never surface.
export const ERR_ABORTED = -3;

// shouldSurfaceFailLoad decides whether a WebContents `did-fail-load` event is
// a genuine, user-visible navigation failure (issue #46 point 3: the event was
// unhandled entirely, so a live URL view could go blank with zero signal).
// Two benign cases must NOT surface: ERR_ABORTED (any cancelled/superseded
// navigation) and any subframe failure (isMainFrame false — an ad iframe or
// tracking pixel failing is not "the page failed to load"). Pure so the filter
// is pinned by a test rather than only observable by loading pages in the live
// app.
export function shouldSurfaceFailLoad(errorCode: number, isMainFrame: boolean): boolean {
  return isMainFrame && errorCode !== ERR_ABORTED;
}

// failLoadMessage formats the notice text for a genuine main-frame load
// failure, including the URL that failed so the user knows which tile/address
// is affected.
export function failLoadMessage(validatedURL: string, errorDescription: string, errorCode: number): string {
  const reason = errorDescription || `error ${errorCode}`;
  return `page failed to load (${reason}): ${validatedURL}`;
}

// renderProcessGoneMessage formats the notice text for a live view whose
// renderer process crashed (`render-process-gone`), which — like did-fail-load
// — was previously unhandled anywhere (issue #46 point 3): the view just went
// blank. url may be unavailable post-crash (best-effort read), in which case
// it's omitted rather than shown as an empty pair of colons.
export function renderProcessGoneMessage(url: string, reason: string): string {
  return url ? `page crashed (${reason}): ${url}` : `page crashed (${reason})`;
}

// rendererLogLine decides whether a renderer console-message (level 0–3:
// verbose, info, warning, error) belongs in the main process's log, and
// formats it if so. The wasm client logs every surfaced notice to its console
// (reportErr, client/wasm/main.go), but that console is invisible outside
// devtools — forwarding warnings and errors makes "all errors are printed to
// the logs" true for the whole renderer, notices included, even after they
// expire off the strip. Info/verbose chatter stays out. Pure so the
// level cut and the prefix are pinned by a test.
export function rendererLogLine(level: number, message: string): string | null {
  if (level < 2) return null;
  return `[renderer:${level === 2 ? 'warning' : 'error'}] ${message}`;
}
