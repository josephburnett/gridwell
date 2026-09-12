import type { Bounds } from './ipc';

// The decisions a live url view obeys, apart from webviews.ts so they run under
// `node --test` with no Electron import.

// The one Electron partition for live url tiles, so a login made in one holds
// in all. `persist:` keeps it on disk.
export const SESSION_PARTITION = 'persist:gridwell';



// 'openExternal' is Chromium handing a non-web protocol such as mailto: to the
// OS default browser. Nothing else is denied.
export function allowPermission(permission: string): boolean {
  return permission !== 'openExternal';
}

// The url a denied popup opens in the pane below, or null: a non-web protocol
// would re-enter the path allowPermission blocks.
export function openBelowUrl(target: string): string | null {
  return /^https?:\/\//i.test(target) ? target : null;
}

// Strips `Electron/<ver>` and `<AppName>/<ver>`, so a site that gates on an
// unknown browser sees plain Chrome.
export function sanitizeUserAgent(ua: string, appName: string): string {
  let out = ua.replace(/\sElectron\/\S+/g, '');
  if (appName) {
    const esc = appName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    out = out.replace(new RegExp(`\\s${esc}\\/\\S+`, 'g'), '');
  }
  return out.replace(/\s+/g, ' ').trim();
}

// roundBounds has a 1px floor because some platforms reject a 0-sized view.
export function roundBounds(b: Bounds): Bounds {
  return {
    x: Math.round(b.x),
    y: Math.round(b.y),
    width: Math.max(1, Math.round(b.width)),
    height: Math.max(1, Math.round(b.height)),
  };
}

// boundsEqual lets the registry skip a redundant setBounds each render frame.
export function boundsEqual(a: Bounds, b: Bounds): boolean {
  return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height;
}

// A copy of the chord's key set, owned by the Key* constants in
// client/contentzoom: a live view holds OS keyboard focus, so main recognizes
// the chord here before forwarding it, and Go and TypeScript share no source.
// Diverge and the two focus states zoom differently; the drift lint in
// gesture-threshold.test.ts pins this copy.
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

// Far enough off any display that a parked view is invisible. Parking rather
// than destroying keeps the page running.
export const PARK_COORD = -100000;

// Where a hidden view goes so canvas overlays can paint over it. Size is kept,
// so un-parking is only a move.
export function parkedBounds(width: number, height: number): Bounds {
  return { x: PARK_COORD, y: PARK_COORD, width, height };
}

// The narrowest layout width in CSS px a live url view renders at; below it the
// page zooms to fit rather than reflowing to a cramped semi-mobile layout.
export const URL_MIN_LAYOUT_WIDTH = 800;

// A WebContentsView cannot render wider than its bounds and be clipped, so
// scaling the page to fit is the closest thing to a min width.
export function minWidthZoomFactor(width: number, minWidth: number): number {
  return width >= minWidth ? 1 : Math.max(0.25, width / minWidth);
}

// Chromium's pageState is dropped, so the blob stays small and stable.
interface UrlHistory {
  index: number;
  entries: { url: string; title: string }[];
}

// The newest entries ending at the active index survive a cap.
const URL_HISTORY_CAP = 50;

// '' for a single entry, which a plain loadURL restores identically.
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

// Restore the persisted back-stack, or plain-load the address. The user can
// edit the address while only the freeze writeback touches the stack, so the
// address wins a disagreement.
export function reviveNavigation(
  url: string,
  history: string | undefined,
): { kind: 'restore'; history: UrlHistory } | { kind: 'load' } {
  const h = parseHistory(history);
  if (!h) return { kind: 'load' };
  if (url !== '' && h.entries[h.index].url !== url) return { kind: 'load' };
  return { kind: 'restore', history: h };
}

// null for a corrupt blob, which then falls back to a plain loadURL.
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

// Two independent facts multiplied, clamped to Chromium's zoom floor.
export function composeZoom(minWidthZoom: number, userZoom: number): number {
  const u = userZoom > 0 ? userZoom : 1;
  return Math.max(0.25, minWidthZoom * u);
}

// How far in CSS px a right-button press must move to become a pane gesture. It
// mirrors dragThreshold in client/wasm/main.go so the two feel identical.
const RIGHT_DRAG_THRESHOLD = 4;

// The minimum hold in ms before a distance-exceeding move counts as a gesture,
// so a trackpad tap that drifts is still a click. Mirrored in
// urlview-preload.ts, which cannot import; gesture-threshold.test.ts lints it.
const RIGHT_DRAG_TIME_MS = 200;

// The distance in CSS px past which the time gate does not apply, so a fast
// flick is a gesture and not a click.
const RIGHT_DRAG_FAR_THRESHOLD = 24;


// classifyRightPress returns true for a drag. urlview-preload.ts inlines the
// same logic because it cannot import.
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

// Chromium's code for a cancelled or superseded navigation. did-fail-load fires
// for it during ordinary browsing, so it must never surface.
const ERR_ABORTED = -3;

// Whether a `did-fail-load` is a failure the user must see; a subframe failure
// is not the page failing.
export function shouldSurfaceFailLoad(errorCode: number, isMainFrame: boolean): boolean {
  return isMainFrame && errorCode !== ERR_ABORTED;
}

// The url is in the notice so the user knows which tile it is about.
export function failLoadMessage(validatedURL: string, errorDescription: string, errorCode: number): string {
  const reason = errorDescription || `error ${errorCode}`;
  return `page failed to load (${reason}): ${validatedURL}`;
}

// The url may be unreadable after a crash, and is then omitted.
export function renderProcessGoneMessage(url: string, reason: string): string {
  return url ? `page crashed (${reason}): ${url}` : `page crashed (${reason})`;
}

// null below warning (levels run 0 to 3). The wasm client's own console is
// invisible outside devtools, so forwarding keeps the failure in the log.
export function rendererLogLine(level: number, message: string): string | null {
  if (level < 2) return null;
  return `[renderer:${level === 2 ? 'warning' : 'error'}] ${message}`;
}
