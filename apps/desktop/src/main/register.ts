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
  ReloadArgs,
  FreezeResult,
  ViewRightdown,
  ViewTouchScroll,
  ErrorEvent,
  NoticeSeverity,
  ChoiceMenuArgs,
  MirroredArgs,
  MoveArgs,
} from './ipc';
import { toContentPoint } from './viewutil';
import { choiceMenuTemplate } from './contextmenu';
import { WebviewRegistry } from './webviews';
import { handOverTrace, logLine, trace } from './trace';
import { menuChose, menuOpened } from './viewtrace';

// The renderer's declared choices, as a trace record names them; webviews.ts
// names the live view's page menu.
const CHOICE_MENU = 'choice';

// safeSend is the one guard every main-to-renderer push goes through: the
// window can close mid-flight, and .send on a destroyed WebContents throws.
function safeSend(wc: WebContents, channel: string, payload: unknown): void {
  if (!wc.isDestroyed()) wc.send(channel, payload);
}

// registerWebviewIpc connects the renderer-facing IPC channels to the registry,
// once, after the root window is created. win is the window toContentPoint
// re-aims a press against; onMirrored takes the mirror pump's pane set.
export function registerWebviewIpc(
  registry: WebviewRegistry,
  rootWC: WebContents,
  win: BaseWindow,
  onMirrored: (paneIds: string[]) => void,
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
    return registry.place(a.paneId, a.tileId, a.url, a.bounds, a.contentZoom ?? 0, a.history ?? '', a.durable ?? false, a.hidden ?? false, a.focused ?? false, a.gen ?? 0);
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

  ipcMain.handle(CH.move, (_e, a: MoveArgs): void => {
    registry.move(a.fromPaneId, a.toPaneId, a.bounds, a.durable, a.hidden, a.focused);
  });

  ipcMain.handle(CH.remove, async (_e, a: RemoveArgs): Promise<FreezeResult> => {
    return registry.remove(a.paneId);
  });

  ipcMain.handle(CH.goBack, (_e, a: PaneRef): void => {
    registry.goBack(a.paneId);
  });

  ipcMain.handle(CH.reload, (_e, a: ReloadArgs): void => {
    registry.reload(a.paneId, a.url);
  });

  ipcMain.handle(CH.showMenu, (_e, a: PaneRef): void => {
    registry.showMenu(a.paneId);
  });

  ipcMain.handle(CH.setMirrored, (_e, a: MirroredArgs): void => {
    onMirrored(a.paneIds);
  });

  // A menu of choices the renderer declared. The answer is the chosen id, or
  // null when the menu closes untouched, so the renderer changes nothing on a
  // dismissal. Both doors settle the same promise once, because a click and
  // the close callback can both fire.
  ipcMain.handle(CH.choiceMenu, (_e, a: ChoiceMenuArgs): Promise<string | null> => {
    return new Promise((resolve) => {
      trace(menuOpened(CHOICE_MENU));
      let settled = false;
      const done = (id: string | null) => {
        if (settled) return;
        settled = true;
        // A dismissal is a row of its own, because a menu that changed nothing
        // and a menu whose row did nothing look the same from outside.
        trace(menuChose(CHOICE_MENU, id ?? 'dismissed'));
        resolve(id);
      };
      const template = choiceMenuTemplate(a.items, done);
      const menu = Menu.buildFromTemplate(template as MenuItemConstructorOptions[]);
      menu.popup({ window: win, callback: () => done(null) });
    });
  });

  ipcMain.handle(CH.handOverTrace, (): Promise<string> => handOverTrace());
}

// forwarder pushes a registry callback's event onto its EV channel unchanged;
// ipc.ts pairs each channel with the shape it carries.
export function forwarder(rootWC: WebContents, channel: string): (ev: unknown) => void {
  return (ev) => safeSend(rootWC, channel, ev);
}

// sendError is the one main-process entry point onto EV.error, so
// client/errsurface is the single place such a notice becomes visible. The
// caller says how loudly; nothing here reads the message to guess.
export function sendError(rootWC: WebContents, source: string, message: string, severity: NoticeSeverity): void {
  // The log too, because safeSend no-ops once the renderer is gone.
  logLine(severity === 'info' ? 'log' : 'error', `[gridwell] ${source}: ${message}`);
  const ev: ErrorEvent = { source, message, severity };
  safeSend(rootWC, EV.error, ev);
}
