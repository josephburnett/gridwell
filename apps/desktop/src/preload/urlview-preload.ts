// urlview-preload.ts — injected into every live URL WebContentsView.
//
// It distinguishes a right-CLICK from a right-DRAG over live web content:
//   - a plain right-click passes straight through, so the page's `contextmenu`
//     event fires and Electron emits `context-menu` on the webContents, which
//     webviews.ts handles to pop the menu (copy link, copy, back, …). Electron
//     has no default page menu — that handler is what makes a right-click do
//     anything at all; this preload's job is just to NOT suppress it.
//   - a right-DRAG arms a Gridwell pane gesture (split / swap / resize /
//     ascend), forwarded to main at the press point — the same way the shell's
//     xterm overlay forwards its right button.
// The decision is deferred: nothing is suppressed on right-down. Only once the
// cursor moves past the drag threshold (with the right button still held) is
// the gesture forwarded and the would-be context menu suppressed. Middle button
// is always ascend. Left button, wheel, keyboard, and selection stay with the
// page.
//
// This runs inside arbitrary web pages, so the view stays sandboxed
// (nodeIntegration:false, contextIsolation:true). A sandboxed/isolated preload
// may use electron's ipcRenderer but may NOT require local modules, so the
// channel names and the threshold are duplicated from ../main/ipc.ts and
// ../main/viewutil.ts (classifyRightPress) rather than imported.
import { ipcRenderer } from 'electron';

// Keep in sync with VIEW.rightdown / VIEW.middledown / VIEW.leftdown /
// VIEW.touchscroll in ../main/ipc.ts.
const VIEW_RIGHTDOWN = 'gw:view-rightdown';
const VIEW_MIDDLEDOWN = 'gw:view-middledown';
const VIEW_LEFTDOWN = 'gw:view-leftdown';
const VIEW_TOUCHSCROLL = 'gw:view-touchscroll';
// Keep in sync with RIGHT_DRAG_THRESHOLD in ../main/viewutil.ts (and the canvas
// dragThreshold). classifyRightPress's logic is inlined below (can't import here).
const RIGHT_DRAG_THRESHOLD = 4;
// Keep in sync with RIGHT_DRAG_TIME_MS in ../main/viewutil.ts.
// A right-button press must be held for at least this many ms AND exceed the
// distance threshold before it counts as a pane gesture. A fast trackpad tap
// that drifts a few pixels past the threshold is still a click, not a drag.
const RIGHT_DRAG_TIME_MS = 200;
// Mirrors viewutil.RIGHT_DRAG_FAR_THRESHOLD: distance past this is a drag
// regardless of duration — a fast flick is a gesture, not a click (#119).
const RIGHT_DRAG_FAR_THRESHOLD = 24;
// MouseEvent.buttons bit for the secondary (right) button.
const RIGHT_BUTTON_MASK = 2;

// Deferred right-button state. The press point is captured at right-down; the
// gesture is forwarded only if the cursor drags past the threshold AND the
// button has been held for at least RIGHT_DRAG_TIME_MS.
let rightDown = false;
let rightDragged = false;
let rightStartX = 0;
let rightStartY = 0;
let rightDownTime = 0;

// Capture phase at the window: fires before the page's own listeners. screenX/
// screenY are physical screen pixels — unaffected by the page's zoomFactor —
// which main converts to window coords via getContentBounds.
window.addEventListener(
  'mousedown',
  (e: MouseEvent) => {
    if (e.button === 2) {
      // Don't suppress: a plain right-click must reach the page. Record the
      // start and let mousemove decide whether this becomes a drag gesture.
      rightDown = true;
      rightDragged = false;
      rightStartX = e.screenX;
      rightStartY = e.screenY;
      rightDownTime = Date.now();
    } else if (e.button === 1) {
      e.preventDefault();
      e.stopPropagation();
      ipcRenderer.send(VIEW_MIDDLEDOWN, { sx: e.screenX, sy: e.screenY });
    } else if (e.button === 0) {
      // Left button: forward a focus intent to main → renderer. The click
      // is NOT suppressed (no preventDefault) — in-page interaction, selection,
      // and links all stay with the page. The renderer only transfers pane focus.
      ipcRenderer.send(VIEW_LEFTDOWN, { sx: e.screenX, sy: e.screenY });
    }
  },
  true,
);

window.addEventListener(
  'mousemove',
  (e: MouseEvent) => {
    if (!rightDown) return;
    // Right button released elsewhere (e.g. the view parked after a prior
    // drag): clear the stale state so a later move can't fake a drag.
    if ((e.buttons & RIGHT_BUTTON_MASK) === 0) {
      rightDown = false;
      return;
    }
    if (rightDragged) return;
    const dx = e.screenX - rightStartX;
    const dy = e.screenY - rightStartY;
    // A drag needs distance past the threshold AND the button held for
    // RIGHT_DRAG_TIME_MS — gating out a fast trackpad tap that drifts a few
    // pixels, which must still produce the native context menu — OR distance
    // past the FAR threshold alone: no tap drifts that far, so a fast flick
    // arms the pane gesture instead of popping the menu (issue #119).
    // Mirrors viewutil.classifyRightPress (preloads can't import).
    const d2 = dx * dx + dy * dy;
    if (
      d2 > RIGHT_DRAG_FAR_THRESHOLD * RIGHT_DRAG_FAR_THRESHOLD ||
      (d2 > RIGHT_DRAG_THRESHOLD * RIGHT_DRAG_THRESHOLD &&
        Date.now() - rightDownTime >= RIGHT_DRAG_TIME_MS)
    ) {
      // Crossed both thresholds → a pane gesture. Forward the ORIGINAL press
      // point so main classifies it where the press began; main then parks the
      // view, so the rest of the drag lands on the canvas.
      rightDragged = true;
      ipcRenderer.send(VIEW_RIGHTDOWN, { sx: rightStartX, sy: rightStartY });
    }
  },
  true,
);

window.addEventListener(
  'mouseup',
  (e: MouseEvent) => {
    if (e.button === 2) rightDown = false;
  },
  true,
);

// ── single-finger touch scroll ──────────────────────────────────────────────
// Chromium does not synthesize scroll gestures from raw touches inside an
// embedded WebContentsView (they reach the page as TouchEvents and then
// nothing scrolls), so a finger drag over a live site did nothing. Convert
// the drag ourselves: forward each move's delta to main, which injects an
// equivalent mouseWheel back into this view at the finger's position —
// scrolling whatever scrollable element is under the finger, exactly as a
// wheel would. preventDefault on the claimed moves keeps Chromium's
// touch→mouse compat from turning the drag into a text selection (and, on a
// platform where native touch scrolling DID work, from scrolling twice).
// Multi-finger touches are left alone (pinch zoom stays the page's).
let touchScrolling = false;
let touchLastX = 0;
let touchLastY = 0;

window.addEventListener(
  'touchstart',
  (e: TouchEvent) => {
    if (e.touches.length !== 1) {
      touchScrolling = false;
      return;
    }
    touchScrolling = true;
    touchLastX = e.touches[0].screenX;
    touchLastY = e.touches[0].screenY;
  },
  true,
);

window.addEventListener(
  'touchmove',
  (e: TouchEvent) => {
    if (!touchScrolling || e.touches.length !== 1) return;
    const t = e.touches[0];
    const dx = t.screenX - touchLastX;
    const dy = t.screenY - touchLastY;
    touchLastX = t.screenX;
    touchLastY = t.screenY;
    if (dx === 0 && dy === 0) return;
    e.preventDefault();
    ipcRenderer.send(VIEW_TOUCHSCROLL, { sx: t.screenX, sy: t.screenY, dx, dy });
  },
  // Non-passive: window touch listeners default to passive, and a passive
  // listener's preventDefault is ignored.
  { capture: true, passive: false },
);

window.addEventListener(
  'touchend',
  (e: TouchEvent) => {
    if (e.touches.length === 0) touchScrolling = false;
  },
  true,
);

// Suppress the context menu only when the right press became a drag (a Gridwell
// gesture): preventing the page's `contextmenu` event stops Chromium from
// emitting the webContents `context-menu` event, so webviews.ts pops no menu
// mid-gesture. A plain right-click falls through, so that handler builds the
// menu. (On Linux/Windows `contextmenu` fires on right-button-up, after the
// drag has been detected.)
window.addEventListener(
  'contextmenu',
  (e: Event) => {
    if (rightDragged) e.preventDefault();
  },
  true,
);
