import { ipcMain, BaseWindow, InputEvent, IpcMainEvent, IpcMainInvokeEvent, Menu, MenuItemConstructorOptions, MouseInputEvent, WebContents } from 'electron';
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
  ViewTouchScroll,
  ErrorEvent,
  NoticeSeverity,
  ChoiceMenuArgs,
  MirroredArgs,
  MoveArgs,
  KeyboardArgs,
} from './ipc';
import { forwardedDragStep, ipcRefusal, IpcSender, toContentPoint } from './viewutil';
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
// once, after the root window is created. origin is the client's, the one page
// the root may hold; win is the window toContentPoint re-aims a drag's moves
// against; onMirrored takes the mirror pump's pane set.
export function registerWebviewIpc(
  registry: WebviewRegistry,
  rootWC: WebContents,
  origin: string,
  win: BaseWindow,
  onMirrored: (paneIds: string[]) => void,
): void {
  // Every handler is registered through on or handle, so none runs for a
  // sender viewutil.ipcRefusal refuses.
  const refused = (channel: string, who: IpcSender, e: IpcMainEvent | IpcMainInvokeEvent): string | null => {
    const frame = e.senderFrame;
    const facts = { rootMainFrame: e.sender === rootWC && !!frame && !frame.parent, url: frame?.url ?? '', liveView: registry.holdsView(e.sender) };
    const why = ipcRefusal(who, facts, origin);
    if (why) logLine('error', `[gridwell] ${channel} refused: ${why}`);
    return why;
  };
  const on = <A>(channel: string, fn: (e: IpcMainEvent, a: A) => void): void => {
    ipcMain.on(channel, (e, a: A) => {
      if (!refused(channel, 'view', e)) fn(e, a);
    });
  };
  const handle = <A, R>(channel: string, fn: (a: A) => R): void => {
    ipcMain.handle(channel, (e, a: A) => {
      const why = refused(channel, 'root', e);
      if (why) throw new Error(`${channel} refused: ${why}`);
      return fn(a);
    });
  };

  // A right press main sent as EV.press became a drag, which the view that
  // took the press keeps, parked or not, and its page sees none of outside its
  // bounds; see forwardedDragStep.
  on(VIEW.rightdown, ({ sender: view }): void => {
    safeSend(rootWC, EV.rightForward, {});
    const onInput = (_e: unknown, ie: InputEvent): void => {
      const m = ie as Partial<MouseInputEvent>;
      const { to, over } = forwardedDragStep(ie.type, m.button);
      if (to) safeSend(rootWC, to, toContentPoint(win, { sx: m.globalX ?? 0, sy: m.globalY ?? 0 }));
      if (over) view.off('input-event', onInput);
    };
    view.on('input-event', onInput);
  });

  // The registry injects an equivalent mouseWheel back into the view, because
  // Chromium will not gesture-scroll raw touches there.
  on(VIEW.touchscroll, (event, p: ViewTouchScroll): void => {
    registry.touchScroll(event.sender, p);
  });

  handle(CH.place, (a: PlaceArgs): Promise<void> => {
    return registry.place(a.paneId, a.tileId, a.url, a.bounds, a.contentZoom ?? 0, a.history ?? '', a.hidden ?? false, a.gen ?? 0);
  });

  handle(CH.setZoom, (a: SetZoomArgs): void => {
    registry.setZoom(a.paneId, a.zoom);
  });

  handle(CH.setBounds, (a: SetBoundsArgs): void => {
    registry.setBounds(a.paneId, a.bounds);
  });

  handle(CH.setHidden, (a: SetHiddenArgs): void => {
    registry.setHidden(a.paneId, a.hidden);
  });

  handle(CH.move, (a: MoveArgs): void => {
    registry.move(a.fromPaneId, a.toPaneId, a.bounds, a.hidden);
  });

  handle(CH.setKeyboard, (a: KeyboardArgs): void => {
    registry.setKeyboard(a.paneId);
  });

  handle(CH.remove, async (a: RemoveArgs): Promise<FreezeResult> => {
    return registry.remove(a.paneId);
  });

  handle(CH.goBack, (a: PaneRef): void => {
    registry.goBack(a.paneId);
  });

  handle(CH.reload, (a: ReloadArgs): void => {
    registry.reload(a.paneId, a.url);
  });

  handle(CH.showMenu, (a: PaneRef): void => {
    registry.showMenu(a.paneId);
  });

  handle(CH.setMirrored, (a: MirroredArgs): void => {
    onMirrored(a.paneIds);
  });

  // A menu of choices the renderer declared. The answer is the chosen id, or
  // null when the menu closes untouched, so the renderer changes nothing on a
  // dismissal. Both paths settle the same promise once, because a click and
  // the close callback can both fire.
  handle(CH.choiceMenu, (a: ChoiceMenuArgs): Promise<string | null> => {
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

  handle(CH.handOverTrace, (): Promise<string> => handOverTrace());
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
