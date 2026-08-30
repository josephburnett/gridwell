// urlview-preload.ts is injected into every live url WebContentsView.
//
// It tells a right-click from a right-drag over live web content:
//   - a plain right-click passes straight through, so the page's `contextmenu`
//     event fires and Electron emits `context-menu` on the webContents, which
//     webviews.ts handles to pop the menu (copy link, copy, back, …). Electron
//     has no default page menu, so that handler is what makes a right-click do
//     anything at all, and this preload's job is not to suppress it.
//   - a right-drag arms a pane gesture (split, swap, resize, ascend), forwarded
//     to main at the press point.
// The decision is deferred: nothing is suppressed on right-down. Only once the
// cursor moves past the drag threshold with the right button still held is the
// gesture forwarded and the would-be context menu suppressed. The middle button
// is always ascend. Left button, wheel, keyboard, and selection stay with the
// page.
//
// This runs inside arbitrary web pages, so the view stays sandboxed
// (nodeIntegration:false, contextIsolation:true). A sandboxed, isolated preload
// may use electron's ipcRenderer but may not require local modules, so the
// channel names and the thresholds are duplicated from ../main/ipc.ts and
// ../main/viewutil.ts rather than imported.
import { ipcRenderer } from 'electron';

// Keep in sync with VIEW.rightdown / VIEW.middledown / VIEW.leftdown /
// VIEW.touchscroll in ../main/ipc.ts.
const VIEW_RIGHTDOWN = 'gw:view-rightdown';
const VIEW_MIDDLEDOWN = 'gw:view-middledown';
const VIEW_LEFTDOWN = 'gw:view-leftdown';
const VIEW_TOUCHSCROLL = 'gw:view-touchscroll';
// Keep in sync with RIGHT_DRAG_THRESHOLD in ../main/viewutil.ts and the canvas
// dragThreshold. classifyRightPress's logic is inlined below; it cannot be
// imported here.
const RIGHT_DRAG_THRESHOLD = 4;
// Keep in sync with RIGHT_DRAG_TIME_MS in ../main/viewutil.ts.
// A right-button press must be held for at least this many ms AND exceed the
// distance threshold before it counts as a pane gesture. A fast trackpad tap
// that drifts a few pixels past the threshold is still a click, not a drag.
const RIGHT_DRAG_TIME_MS = 200;
// Mirrors viewutil.RIGHT_DRAG_FAR_THRESHOLD: distance past this is a drag
// regardless of duration, so a fast flick is a gesture rather than a click.
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

// Capture phase at the window, so this fires before the page's own listeners.
// screenX and screenY are physical screen pixels, unaffected by the page's
// zoomFactor; main converts them to window coords through getContentBounds.
window.addEventListener(
  'mousedown',
  (e: MouseEvent) => {
    if (e.button === 2) {
      // Do not suppress: a plain right-click must reach the page. Record the
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
      // Left button: forward a focus intent to main, which relays it to the
      // renderer. The click is not suppressed, so in-page interaction,
      // selection, and links stay with the page; the renderer only transfers
      // pane focus.
      ipcRenderer.send(VIEW_LEFTDOWN, { sx: e.screenX, sy: e.screenY });
    }
  },
  true,
);

window.addEventListener(
  'mousemove',
  (e: MouseEvent) => {
    if (!rightDown) return;
    // The right button was released elsewhere, for instance while the view was
    // parked after a prior drag. Clear the stale state so a later move cannot
    // fake a drag.
    if ((e.buttons & RIGHT_BUTTON_MASK) === 0) {
      rightDown = false;
      return;
    }
    if (rightDragged) return;
    const dx = e.screenX - rightStartX;
    const dy = e.screenY - rightStartY;
    // A drag needs distance past the threshold and the button held for
    // RIGHT_DRAG_TIME_MS, which gates out a fast trackpad tap that drifts a few
    // pixels and must still produce the native context menu. Distance past the
    // far threshold alone also counts: no tap drifts that far, so a fast flick
    // arms the pane gesture instead of popping the menu. Mirrors
    // viewutil.classifyRightPress, which a preload cannot import.
    const d2 = dx * dx + dy * dy;
    if (
      d2 > RIGHT_DRAG_FAR_THRESHOLD * RIGHT_DRAG_FAR_THRESHOLD ||
      (d2 > RIGHT_DRAG_THRESHOLD * RIGHT_DRAG_THRESHOLD &&
        Date.now() - rightDownTime >= RIGHT_DRAG_TIME_MS)
    ) {
      // This is a pane gesture. Forward the original press point so main
      // classifies it where the press began; main then parks the view, so the
      // rest of the drag lands on the canvas.
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
// embedded WebContentsView: they reach the page as TouchEvents and nothing
// scrolls. So the drag is converted here. Each move's delta goes to main, which
// injects an equivalent mouseWheel back into this view at the finger's
// position, scrolling whatever scrollable element is under the finger exactly
// as a wheel would. preventDefault on the claimed moves keeps Chromium's
// touch-to-mouse compatibility from turning the drag into a text selection, and
// keeps a platform with working native touch scrolling from scrolling twice.
// Multi-finger touches are left alone, so pinch zoom stays the page's.
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

// Suppress the context menu only when the right press became a drag, that is a
// pane gesture. Preventing the page's `contextmenu` event stops Chromium from
// emitting the webContents `context-menu` event, so webviews.ts pops no menu
// mid-gesture. A plain right-click falls through and that handler builds the
// menu. On Linux and Windows `contextmenu` fires on right-button-up, after the
// drag has been detected.
window.addEventListener(
  'contextmenu',
  (e: Event) => {
    if (rightDragged) e.preventDefault();
  },
  true,
);
