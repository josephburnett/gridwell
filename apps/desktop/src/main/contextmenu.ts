// contextmenu.ts — builds the context menu for a live URL WebContentsView.
//
// Electron's WebContentsView has NO built-in context menu: right-clicking a
// page emits a `context-menu` event on the webContents, and if nothing handles
// it nothing appears. So "copy this link" only works if WE build and pop the
// menu. (This is the bug the file exists to fix: the live URL view forwarded
// right-DRAGs as pane gestures but a plain right-click fell through to a page
// menu that Electron never renders — so it did nothing.)
//
// The TEMPLATE is built here as a pure function so it can be unit-tested
// without Electron: it takes a minimal params subset and a bag of injected
// actions, and returns plain menu items whose `click` calls those actions.
// webviews.ts supplies the real actions (clipboard / webContents) and feeds
// the template to Menu.buildFromTemplate. Keeping the policy (what items, when,
// what each does) here and the Electron plumbing there is the seam that makes
// the menu testable.

// ContextParams is the subset of Electron's ContextMenuParams the menu needs,
// plus the two navigation flags (which live on webContents, not the params).
interface ContextParams {
  // linkURL is the href of an <a> under the cursor, or '' if none.
  linkURL: string;
  // selectionText is the currently-selected text, or '' if none.
  selectionText: string;
  // isEditable is true over an input/textarea/contenteditable.
  isEditable: boolean;
  // editFlags mirror document.queryCommandEnabled for the edit actions.
  editFlags: { canCut: boolean; canCopy: boolean; canPaste: boolean };
  // canGoBack/canGoForward gate the navigation items (from navigationHistory).
  canGoBack: boolean;
  canGoForward: boolean;
  // canFreeze is whether the view's tile is DURABLE: an ephemeral visit has
  // nothing to re-descend into, so it gets no Freeze Page item (issue #240).
  canFreeze: boolean;
}

// ContextActions are the effects the menu items invoke. Injected so the
// template stays pure: webviews.ts wires these to clipboard + the view's
// webContents; the unit test wires spies.
interface ContextActions {
  copyText(text: string): void;
  copyLink(url: string): void;
  openLink(url: string): void;
  cut(): void;
  paste(): void;
  back(): void;
  forward(): void;
  reload(): void;
  // (clearSiteData is gone, issue #240: clearing browser state is the
  // `gridwell clear-browser-data` CLI now — an operator action on the
  // profile, not an in-page gesture.)
  // freeze is the explicit freeze gesture (issue #237): tear the live view
  // down (with the usual freeze writeback) and store the user's standing
  // frozen intent on the tile, so re-descending stays frozen until the
  // reconnect button clears it.
  freeze(): void;
}

// MenuTemplateItem is the structural subset of Electron's
// MenuItemConstructorOptions this builder emits — declared locally so the
// module imports nothing from electron and can run under plain node/tsx in the
// unit test. It is assignable to MenuItemConstructorOptions at the call site.
interface MenuTemplateItem {
  label?: string;
  type?: 'separator';
  enabled?: boolean;
  click?: () => void;
}

// urlContextMenuTemplate assembles the menu for a right-click over live web
// content, in the familiar Chromium order: link actions, then text/edit
// actions, then page navigation. Items that don't apply (no link, no selection,
// not editable) are omitted so the menu never shows dead entries — except the
// always-present navigation block, whose items disable rather than vanish.
export function urlContextMenuTemplate(p: ContextParams, a: ContextActions): MenuTemplateItem[] {
  const items: MenuTemplateItem[] = [];

  if (p.linkURL) {
    items.push({ label: 'Open Link', click: () => a.openLink(p.linkURL) });
    items.push({ label: 'Copy Link Address', click: () => a.copyLink(p.linkURL) });
    items.push({ type: 'separator' });
  }

  if (p.isEditable) {
    items.push({ label: 'Cut', enabled: p.editFlags.canCut, click: () => a.cut() });
    items.push({ label: 'Copy', enabled: p.editFlags.canCopy, click: () => a.copyText(p.selectionText) });
    items.push({ label: 'Paste', enabled: p.editFlags.canPaste, click: () => a.paste() });
    items.push({ type: 'separator' });
  } else if (p.selectionText) {
    items.push({ label: 'Copy', click: () => a.copyText(p.selectionText) });
    items.push({ type: 'separator' });
  }

  items.push({ label: 'Back', enabled: p.canGoBack, click: () => a.back() });
  items.push({ label: 'Forward', enabled: p.canGoForward, click: () => a.forward() });
  items.push({ label: 'Reload', click: () => a.reload() });
  if (p.canFreeze) {
    items.push({ label: 'Freeze Page', click: () => a.freeze() });
  }

  return items;
}
