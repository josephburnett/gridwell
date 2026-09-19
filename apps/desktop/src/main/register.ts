import { ipcMain, BaseWindow, Menu, MenuItemConstructorOptions, WebContents } from 'electron';
import {
  CH,
  EV,
  VIEW,
  PlaceArgs,
  SetBoundsArgs,
  SetHiddenArgs,
  SetZoomArgs,
  RemoveArgs,
  PaneRef,
  FreezeResult,
  ViewRightdown,
  ViewTouchScroll,
  ForwardedRightdown,
  ErrorEvent,
  OpenBelowEvent,
  FreezeURLEvent,
  ContextMenuEvent,
  ZoomKeyEvent,
  ChoiceMenuArgs,
} from './ipc';
import { choiceMenuTemplate } from './contextmenu';
import { WebviewRegistry } from './webviews';

// safeSend is the one guard every main-to-renderer push goes through: the
// window can close mid-flight, and .send on a destroyed WebContents throws.
function safeSend(wc: WebContents, channel: string, payload: unknown): void {
  if (!wc.isDestroyed()) wc.send(channel, payload);
}

// registerWebviewIpc connects the renderer-facing IPC channels to the registry,
// once, after the root window is created. win's content bounds convert a
// screen-space press into canvas coordinates.
export function registerWebviewIpc(
  registry: WebviewRegistry,
  rootWC: WebContents,
  win: BaseWindow,
): void {
  // Begins a pane gesture; the renderer parks the view so the rest of the drag
  // lands on the canvas.
  ipcMain.on(VIEW.rightdown, (_event, p: ViewRightdown): void => {
    const cb = win.getContentBounds();
    safeSend(rootWC, EV.rightForward, { x: p.sx - cb.x, y: p.sy - cb.y });
  });

  ipcMain.on(VIEW.middledown, (_event, p: ViewRightdown): void => {
    const cb = win.getContentBounds();
    safeSend(rootWC, EV.middleForward, { x: p.sx - cb.x, y: p.sy - cb.y });
  });

  // A focus-transfer intent; the preload does not suppress it, so the click
  // still reaches the page.
  ipcMain.on(VIEW.leftdown, (_event, p: ViewRightdown): void => {
    const cb = win.getContentBounds();
    const fwd: ForwardedRightdown = { x: p.sx - cb.x, y: p.sy - cb.y };
    safeSend(rootWC, EV.leftForward, fwd);
  });

  // The registry injects an equivalent mouseWheel back into the view, because
  // Chromium will not gesture-scroll raw touches there.
  ipcMain.on(VIEW.touchscroll, (event, p: ViewTouchScroll): void => {
    registry.touchScroll(event.sender, p);
  });

  ipcMain.handle(CH.place, (_e, a: PlaceArgs): Promise<void> => {
    return registry.place(a.paneId, a.tileId, a.url, a.bounds, a.contentZoom ?? 0, a.history ?? '', a.durable ?? false, a.hidden ?? false, a.focused ?? false);
  });

  ipcMain.handle(CH.setZoom, (_e, a: SetZoomArgs): void => {
    registry.setZoom(a.paneId, a.zoom);
  });

  ipcMain.handle(CH.setBounds, (_e, a: SetBoundsArgs): void => {
    registry.setBounds(a.paneId, a.bounds);
  });

  ipcMain.handle(CH.setHidden, (_e, a: SetHiddenArgs): void => {
    registry.setHidden(a.paneId, a.hidden, a.focused);
  });

  ipcMain.handle(CH.remove, async (_e, a: RemoveArgs): Promise<FreezeResult> => {
    return registry.remove(a.paneId);
  });

  ipcMain.handle(CH.goBack, (_e, a: PaneRef): void => {
    registry.goBack(a.paneId);
  });

  ipcMain.handle(CH.showMenu, (_e, a: PaneRef): void => {
    registry.showMenu(a.paneId);
  });

  // A menu of choices the renderer declared. The answer is the chosen id, or
  // null when the menu closes untouched, so the renderer changes nothing on a
  // dismissal. Both doors settle the same promise once, because a click and
  // the close callback can both fire.
  ipcMain.handle(CH.choiceMenu, (_e, a: ChoiceMenuArgs): Promise<string | null> => {
    return new Promise((resolve) => {
      let settled = false;
      const done = (id: string | null) => {
        if (settled) return;
        settled = true;
        resolve(id);
      };
      const template = choiceMenuTemplate(a.items, done);
      const menu = Menu.buildFromTemplate(template as MenuItemConstructorOptions[]);
      menu.popup({ window: win, callback: () => done(null) });
    });
  });

}

export function makeNavForwarder(rootWC: WebContents) {
  return (ev: { paneId: string; tileId: string; url: string; title: string }) => {
    safeSend(rootWC, EV.nav, ev);
  };
}

// The renderer splits the pane and opens the link as an ephemeral visit.
export function makeOpenBelowForwarder(rootWC: WebContents): (ev: OpenBelowEvent) => void {
  return (ev) => safeSend(rootWC, EV.openBelow, ev);
}

// The wasm tears the view down and persists the standing frozen intent.
export function makeFreezeURLForwarder(rootWC: WebContents): (ev: FreezeURLEvent) => void {
  return (ev) => safeSend(rootWC, EV.freezeUrl, ev);
}

// focusToPane moves focus to that pane before the menu can act.
export function makeContextMenuForwarder(rootWC: WebContents): (ev: ContextMenuEvent) => void {
  return (ev) => safeSend(rootWC, EV.menuPane, ev);
}

// The renderer's applyContentZoom applies and persists the chord.
export function makeZoomKeyForwarder(rootWC: WebContents): (ev: ZoomKeyEvent) => void {
  return (ev) => safeSend(rootWC, EV.zoomKey, ev);
}

export function sendFrame(rootWC: WebContents, paneId: string, tileId: string, jpegBase64: string): void {
  if (jpegBase64) safeSend(rootWC, EV.frame, { paneId, tileId, jpegBase64 });
}

// sendError is the one main-process entry point onto EV.error, so
// client/errsurface is the single place such a failure becomes visible.
export function sendError(rootWC: WebContents, source: string, message: string): void {
  // The log too, because safeSend no-ops once the renderer is gone.
  console.error(`[gridwell] ${source}: ${message}`);
  const ev: ErrorEvent = { source, message };
  safeSend(rootWC, EV.error, ev);
}
