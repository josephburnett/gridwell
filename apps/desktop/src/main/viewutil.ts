import type { Bounds } from './ipc';

// SESSION_PARTITION is the one Electron partition for live url tiles. Every
// live url tile, local or through a mount, shares this cookie jar and
// DOM-storage area, so a login made in one tile holds in all of them. The
// `persist:` prefix keeps it on disk across app restarts.
export const SESSION_PARTITION = 'persist:gridwell';



// allowPermission decides the live-view session's permission requests.
// 'openExternal' is Chromium handing a non-web protocol (zoommtg:, mailto:, …)
// to the OS: Electron grants it by default and calls shell.openExternal, and on
// Linux xdg-open bounces an unhandled protocol into the default browser, so the
// link opens both in a pane and in an outside browser. A tile is Gridwell's
// only browsing surface, so nothing may escape to the OS. Every other
// permission keeps Electron's default grant.
export function allowPermission(permission: string): boolean {
  return permission !== 'openExternal';
}

// openBelowUrl returns the url a denied popup (window.open, target=_blank)
// should open in the pane below, or null when the target must not open at all.
// Only web urls belong in a tile: a non-web protocol has no in-grid meaning,
// and forwarding it would re-trigger the external-protocol path the permission
// handler blocks.
export function openBelowUrl(target: string): string | null {
  return /^https?:\/\//i.test(target) ? target : null;
}

// sanitizeUserAgent strips the two tokens that mark Chromium's default UA as a
// non-browser embedding, `Electron/<ver>` and the app's own `<AppName>/<ver>`,
// leaving the genuine `Chrome/<ver>` token and everything else intact. A live
// url tile is real Chromium, so dropping the embedding tokens is honest where
// faking another engine would not be; sites that gate on an unknown browser
// then see a plain Chrome string. Applied once as app.userAgentFallback, the
// default for every partition and view. Idempotent: re-running removes nothing.
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

// zoomChordKey normalizes a before-input-event Input to the content-zoom chord
// key it carries ('+', '=', '-', '0'), or '' when the input is not the chord.
// This is the key set the wasm handleContentZoomKey accepts; keep them in step
// or the two focus states zoom differently.
export function zoomChordKey(input: { key: string; control?: boolean; meta?: boolean }): string {
  if (!input.control && !input.meta) return '';
  switch (input.key) {
    case '+':
    case '=':
    case '-':
    case '0':
      return input.key;
  }
  return '';
}

// PARK_COORD is far enough off any display that a parked view/control is not
// visible; the registry parks rather than destroys so the page keeps running.
export const PARK_COORD = -100000;

// parkedBounds is the off-screen rect a view moves to while it is hidden,
// during a drag, gesture, or modal, so canvas overlays can paint where the
// native view sits. Width and height are preserved, both because some platforms
// reject a 0-sized view and so un-parking is a pure move.
export function parkedBounds(width: number, height: number): Bounds {
  return { x: PARK_COORD, y: PARK_COORD, width, height };
}

// URL_MIN_LAYOUT_WIDTH is the narrowest layout width in CSS px a live url view
// renders at; below it the page is zoomed to fit rather than reflowed to a
// cramped semi-mobile layout. 800 keeps a desktop layout, only slightly scaled,
// at ordinary pane widths. It lives here rather than in webviews.ts so a unit
// test can pin the production value.
export const URL_MIN_LAYOUT_WIDTH = 800;

// minWidthZoomFactor is the page zoom that keeps a narrow live url view laying
// out at minWidth instead of reflowing to a cramped mobile layout: 1 at or above
// minWidth, else width/minWidth clamped to a 0.25 floor. A native
// WebContentsView cannot render wider than its bounds and be clipped, so scaling
// the page to fit is the closest thing to a min width with horizontal scroll.
// Pure, so a test pins the clamp and the threshold.
export function minWidthZoomFactor(width: number, minWidth: number): number {
  return width >= minWidth ? 1 : Math.max(0.25, width / minWidth);
}

// UrlHistory is the persisted shape of a url tile's navigation back-stack: the
// entry list and the active index. pageState is stripped, leaving urls and
// titles, so the blob stays small and its shape stable.
interface UrlHistory {
  index: number;
  entries: { url: string; title: string }[];
}

// URL_HISTORY_CAP bounds how many entries a freeze persists. The newest
// entries ending at the active index survive; the index is re-based.
const URL_HISTORY_CAP = 50;

// serializeHistory turns a live navigationHistory snapshot into the persisted
// JSON, capped. Returns '' when there is nothing worth persisting (a single
// entry restores identically via plain loadURL).
export function serializeHistory(
  entries: { url: string; title: string }[],
  index: number,
  cap: number = URL_HISTORY_CAP,
): string {
  if (entries.length < 2) return '';
  let es = entries.map((e) => ({ url: e.url, title: e.title }));
  let idx = Math.min(Math.max(index, 0), es.length - 1);
  if (es.length > cap) {
    const start = Math.max(0, idx - cap + 1);
    es = es.slice(start, start + cap);
    idx = idx - start;
  }
  return JSON.stringify({ index: idx, entries: es });
}

// reviveNavigation decides how a placed url tile comes back: restore the
// persisted back-stack, or plain-load the address. The address (url_string) is
// a fact the user can edit through the content door, while the back-stack is
// written only by the freeze writeback, so the two can disagree. When they do
// the address wins; restoring the stack would navigate to the page the user
// just typed over. Pure, so a unit test pins the tie-break.
export function reviveNavigation(
  url: string,
  history: string | undefined,
): { kind: 'restore'; history: UrlHistory } | { kind: 'load' } {
  const h = parseHistory(history);
  if (!h) return { kind: 'load' };
  if (url !== '' && h.entries[h.index].url !== url) return { kind: 'load' };
  return { kind: 'restore', history: h };
}

// parseHistory validates persisted history JSON back into a restorable shape,
// or null when it is absent or invalid. The caller then falls back to a plain
// loadURL: a corrupt blob must never break revive.
export function parseHistory(json: string | undefined): UrlHistory | null {
  if (!json) return null;
  try {
    const h = JSON.parse(json) as UrlHistory;
    if (!Array.isArray(h.entries) || h.entries.length === 0) return null;
    if (!h.entries.every((e) => typeof e.url === 'string' && e.url !== '')) return null;
    const index = Math.min(Math.max(Number(h.index) || 0, 0), h.entries.length - 1);
    return { index, entries: h.entries.map((e) => ({ url: e.url, title: String(e.title ?? '') })) };
  } catch {
    return null;
  }
}

// composeZoom multiplies the layout min-width zoom with the user content zoom
// (the tile's persisted content_zoom). The two are independent facts and
// neither may overwrite the other. Clamped to Chromium's setZoomFactor floor.
export function composeZoom(minWidthZoom: number, userZoom: number): number {
  const u = userZoom > 0 ? userZoom : 1;
  return Math.max(0.25, minWidthZoom * u);
}

// RIGHT_DRAG_THRESHOLD is how far in CSS px the cursor must move with the right
// button held before a press over a live url view becomes a pane gesture rather
// than a plain right-click. It mirrors the canvas dragThreshold
// (client/wasm/main.go) so live view and canvas feel identical.
const RIGHT_DRAG_THRESHOLD = 4;

// RIGHT_DRAG_TIME_MS is the minimum time in ms a right-button press must be held
// before a distance-exceeding move counts as a pane gesture. A quick trackpad
// tap that drifts a few pixels past the threshold within this window is still a
// click, so the native context menu fires. Mirrored verbatim in
// urlview-preload.ts, which cannot import from here; gesture-threshold.test.ts
// lints both copies.
const RIGHT_DRAG_TIME_MS = 200;

// RIGHT_DRAG_FAR_THRESHOLD is the distance in CSS px beyond which a right-drag
// is unambiguous on its own: no trackpad tap drifts this far, so the time gate
// does not apply. Without it a fast flick, large in distance and short in
// duration, reads as a click and pops the context menu.
const RIGHT_DRAG_FAR_THRESHOLD = 24;


// classifyRightPress returns true, meaning a drag, when the distance and time
// thresholds are both exceeded, or when the distance alone is past the far
// threshold that no accidental tap-drift reaches. So a fast flick is a gesture
// and a jittery trackpad tap stays a click and produces the context menu. Pure
// and unit-tested; urlview-preload.ts inlines equivalent logic because it
// cannot import.
export function classifyRightPress(
  dx: number,
  dy: number,
  durationMs: number,
  distThreshold: number = RIGHT_DRAG_THRESHOLD,
  timeThresholdMs: number = RIGHT_DRAG_TIME_MS,
  farThreshold: number = RIGHT_DRAG_FAR_THRESHOLD,
): boolean {
  const d2 = dx * dx + dy * dy;
  if (d2 > farThreshold * farThreshold) return true;
  return d2 > distThreshold * distThreshold && durationMs >= timeThresholdMs;
}

// ERR_ABORTED is Chromium's net error code for a navigation the page or user
// cancelled: a redirect superseded by another navigation, window.stop(), a
// same-document replace. It is not a failure, and did-fail-load fires for it
// constantly during ordinary navigation, so it must never surface.
const ERR_ABORTED = -3;

// shouldSurfaceFailLoad decides whether a WebContents `did-fail-load` event is a
// genuine, user-visible navigation failure; unhandled, a live url view goes
// blank with no signal. Two benign cases must not surface: ERR_ABORTED, for any
// cancelled or superseded navigation, and any subframe failure, since an ad
// iframe or tracking pixel failing is not the page failing to load. Pure, so a
// test pins the filter.
export function shouldSurfaceFailLoad(errorCode: number, isMainFrame: boolean): boolean {
  return isMainFrame && errorCode !== ERR_ABORTED;
}

// failLoadMessage formats the notice text for a genuine main-frame load
// failure, including the url that failed so the user knows which tile and
// address it is about.
export function failLoadMessage(validatedURL: string, errorDescription: string, errorCode: number): string {
  const reason = errorDescription || `error ${errorCode}`;
  return `page failed to load (${reason}): ${validatedURL}`;
}

// renderProcessGoneMessage formats the notice text for a live view whose
// renderer process crashed (`render-process-gone`); unreported, the view simply
// goes blank. The url may be unreadable after a crash, in which case it is
// omitted rather than shown as an empty pair of colons.
export function renderProcessGoneMessage(url: string, reason: string): string {
  return url ? `page crashed (${reason}): ${url}` : `page crashed (${reason})`;
}

// rendererLogLine decides whether a renderer console-message belongs in the main
// process's log, and formats it if so. Levels run 0 to 3: verbose, info,
// warning, error. The wasm client logs every surfaced notice to its console
// (reportErr, client/wasm/main.go), but that console is invisible outside
// devtools, so forwarding warnings and errors keeps every renderer failure in
// the log even after the notice expires off the strip. Info and verbose chatter
// stays out. Pure, so a test pins the level cut and the prefix.
export function rendererLogLine(level: number, message: string): string | null {
  if (level < 2) return null;
  return `[renderer:${level === 2 ? 'warning' : 'error'}] ${message}`;
}
