// urlview-preload.ts — injected into every live URL WebContentsView.
//
// One job: forward a RIGHT-button mousedown to main (capture phase) so the
// renderer can begin a pane gesture (split / swap / resize / ascend) over live
// web content — the same way the shell's in-renderer xterm overlay forwards
// its right button. Left button, wheel, keyboard, and text selection stay with
// the page, so native browsing is untouched.
//
// This runs inside arbitrary web pages, so the view stays sandboxed
// (nodeIntegration:false, contextIsolation:true). A sandboxed/isolated preload
// may use electron's ipcRenderer but may NOT require local modules, so the
// channel name is duplicated from ipc.ts (VIEW.rightdown) rather than imported.
import { ipcRenderer } from 'electron';

// Keep in sync with VIEW.rightdown in ../main/ipc.ts.
const VIEW_RIGHTDOWN = 'gw:view-rightdown';

// Capture phase at the window: this fires before any of the page's own
// document/element listeners, so stopPropagation keeps the press from ever
// reaching the page, and preventDefault suppresses focus/selection. screenX/
// screenY are physical screen pixels — unaffected by the page's zoomFactor —
// which main converts to window coords via getContentBounds.
window.addEventListener(
  'mousedown',
  (e: MouseEvent) => {
    if (e.button !== 2) return; // left/middle: leave them with the page
    e.preventDefault();
    e.stopPropagation();
    ipcRenderer.send(VIEW_RIGHTDOWN, { sx: e.screenX, sy: e.screenY });
  },
  true,
);

// The right press would otherwise raise the page's native context menu.
window.addEventListener('contextmenu', (e: Event) => e.preventDefault(), true);
