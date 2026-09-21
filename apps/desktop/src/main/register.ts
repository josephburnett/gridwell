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
  ErrorEvent,
  FrameEvent,
  ChoiceMenuArgs,
} from './ipc';
import { toContentPoint } from './viewutil';
import { choiceMenuTemplate } from './contextmenu';
import { WebviewRegistry } from './webviews';

// safeSend is the one guard every main-to-renderer push goes through: the
// window can close mid-flight, and .send on a destroyed WebContents throws.
function safeSend(wc: WebContents, channel: string, payload: unknown): void {
  if (!wc.isDestroyed()) wc.send(channel, payload);
}

// registerWebviewIpc connects the renderer-facing IPC channels to the registry,
// once, after the root window is created. win is the window toContentPoint
// re-aims a press against.
export function registerWebviewIpc(
  registry: WebviewRegistry,
  rootWC: WebContents,
  win: BaseWindow,
): void {
  // The live view swallows the renderer's own mouse events, so its preload
  // sends each press here to be re-aimed at the canvas. What the renderer then
  // does with one is EV's business in ipc.ts.
  for (const [inbound, outbound] of [
    [VIEW.rightdown, EV.rightForward],
    [VIEW.middledown, EV.middleForward],
    [VIEW.leftdown, EV.leftForward],
  ]) {
    ipcMain.on(inbound, (_event, p: ViewRightdown): void => {
      safeSend(rootWC, outbound, toContentPoint(win, p));
    });
  }

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

// forwarder pushes a registry callback's event onto its EV channel unchanged;
// ipc.ts pairs each channel with the shape it carries.
export function forwarder(rootWC: WebContents, channel: string): (ev: unknown) => void {
  return (ev) => safeSend(rootWC, channel, ev);
}

export function sendFrame(rootWC: WebContents, paneId: string, tileId: string, jpegBase64: string): void {
  if (jpegBase64) safeSend(rootWC, EV.frame, { paneId, tileId, jpegBase64 } satisfies FrameEvent);
}

// sendError is the one main-process entry point onto EV.error, so
// client/errsurface is the single place such a failure becomes visible.
export function sendError(rootWC: WebContents, source: string, message: string): void {
  // The log too, because safeSend no-ops once the renderer is gone.
  console.error(`[gridwell] ${source}: ${message}`);
  const ev: ErrorEvent = { source, message };
  safeSend(rootWC, EV.error, ev);
}
