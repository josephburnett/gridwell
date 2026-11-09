// Injected into every live url WebContentsView, where it tells a right-click
// from a right-drag. Nothing is suppressed on right-down: once the cursor
// passes the drag threshold with the button still held, the gesture is
// forwarded and the page sees no contextmenu; a release short of that hands
// the page its contextmenu. Middle is always ascend; left, wheel, keyboard and
// selection stay with the page.
//
// This runs inside arbitrary web pages, so the view stays sandboxed, and a
// sandboxed isolated preload may not require local modules. That is why the
// channel names and thresholds are duplicated from ../main.
import { ipcRenderer } from 'electron';

// Keep in sync with VIEW in ../main/ipc.ts.
const VIEW_RIGHTDOWN = 'gw:view-rightdown';
const VIEW_MIDDLEDOWN = 'gw:view-middledown';
const VIEW_LEFTDOWN = 'gw:view-leftdown';
const VIEW_TOUCHSCROLL = 'gw:view-touchscroll';
// Keep in sync with ../main/viewutil.ts, which owns what each one is for.
// gesture-threshold.test.ts lints the copies.
const RIGHT_DRAG_THRESHOLD = 4;
const RIGHT_DRAG_TIME_MS = 200;
const RIGHT_DRAG_FAR_THRESHOLD = 24;
// MouseEvent.buttons bit for the right button.
const RIGHT_BUTTON_MASK = 2;

let rightDown = false;
let rightDragged = false;
let rightStartX = 0;
let rightStartY = 0;
let rightDownTime = 0;
// A contextmenu that came while the button was down, held until the release
// says whether it was a click.
let menuHeld = false;
// Set while the held contextmenu is handed to the page, so the listener below
// lets it through.
let handingOver = false;

// onPress forwards a press, whichever event heard it. screenX and screenY are
// physical screen pixels, unaffected by zoomFactor.
function onPress(e: MouseEvent): void {
  if (e.button === 2) {
    // Not suppressed: a plain right-click must reach the page.
    rightDown = true;
    rightDragged = false;
    rightStartX = e.screenX;
    rightStartY = e.screenY;
    rightDownTime = Date.now();
    menuHeld = false;
  } else if (e.button === 1) {
    e.preventDefault();
    e.stopPropagation();
    ipcRenderer.send(VIEW_MIDDLEDOWN, { sx: e.screenX, sy: e.screenY });
  } else if (e.button === 0) {
    // A focus intent. Not suppressed, so selection and links stay the page's.
    ipcRenderer.send(VIEW_LEFTDOWN, { sx: e.screenX, sy: e.screenY });
  }
}

// Capture phase at the window, so these fire before the page's listeners. A
// mouse press is heard at its pointerdown, since a page that cancels one makes
// Chromium skip the mousedown; a touch has no mousedown until its tap.
window.addEventListener('pointerdown', (e: PointerEvent) => e.pointerType === 'mouse' && onPress(e), true);
window.addEventListener('mousedown', (e) => (e as any).sourceCapabilities?.firesTouchEvents && onPress(e), true);

window.addEventListener(
  'mousemove',
  (e: MouseEvent) => {
    if (!rightDown) return;
    // The button was released elsewhere, for instance while the view was parked
    // after a prior drag, so a later move must not fake a drag.
    if ((e.buttons & RIGHT_BUTTON_MASK) === 0) {
      rightDown = false;
      return;
    }
    if (rightDragged) return;
    const dx = e.screenX - rightStartX;
    const dy = e.screenY - rightStartY;
    // Mirrors viewutil.classifyRightPress, which a preload cannot import.
    const d2 = dx * dx + dy * dy;
    if (
      d2 > RIGHT_DRAG_FAR_THRESHOLD * RIGHT_DRAG_FAR_THRESHOLD ||
      (d2 > RIGHT_DRAG_THRESHOLD * RIGHT_DRAG_THRESHOLD &&
        Date.now() - rightDownTime >= RIGHT_DRAG_TIME_MS)
    ) {
      // The original press point, so the renderer classifies the gesture
      // where it began. Main follows the rest of it (register.ts).
      rightDragged = true;
      ipcRenderer.send(VIEW_RIGHTDOWN, { sx: rightStartX, sy: rightStartY });
    }
  },
  true,
);

window.addEventListener(
  'mouseup',
  (e: MouseEvent) => {
    if (e.button !== 2) return;
    rightDown = false;
    if (!menuHeld || rightDragged) return;
    menuHeld = false;
    // Handed over inside the release's own dispatch, before the page's mouseup
    // listeners, which is the order a press-time contextmenu has anyway.
    const target = document.elementFromPoint(e.clientX, e.clientY) ?? document.documentElement;
    handingOver = true;
    try {
      target.dispatchEvent(
        new MouseEvent('contextmenu', {
          bubbles: true,
          cancelable: true,
          composed: true,
          button: 2,
          clientX: e.clientX,
          clientY: e.clientY,
          screenX: e.screenX,
          screenY: e.screenY,
        }),
      );
    } finally {
      handingOver = false;
    }
  },
  true,
);

// Chromium does not synthesize scroll gestures from raw touches inside an
// embedded WebContentsView, so each move's delta goes to main, which injects a
// mouseWheel back. preventDefault on a claimed move keeps touch-to-mouse
// compatibility from making it a text selection, and keeps a platform with
// working native touch scrolling from scrolling twice. Multi-finger touches are
// left alone, so pinch zoom stays the page's.
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
  // Window touch listeners default to passive, whose preventDefault is
  // ignored.
  { capture: true, passive: false },
);

window.addEventListener(
  'touchend',
  (e: TouchEvent) => {
    if (e.touches.length === 0) touchScrolling = false;
  },
  true,
);

// Chromium fires contextmenu on the press on some platforms and on the release
// on others. One that comes while the button is down is held, and handed to the
// page on a release without a drag; one that comes after a drag never reaches
// it. Stopping it also stops Chromium emitting the webContents `context-menu`.
window.addEventListener(
  'contextmenu',
  (e: Event) => {
    if (handingOver) return;
    if (rightDown) menuHeld = true;
    else if (!rightDragged) return;
    e.preventDefault();
    e.stopImmediatePropagation();
  },
  true,
);
