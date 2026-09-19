// Which items the two native menus offer. webviews.ts supplies the live url
// view's actions, register.ts the choice menu's; neither menu is built here.

import { ChoiceItem } from './ipc';

// ContextParams is the subset of Electron's ContextMenuParams the menu needs,
// plus the two navigation flags, which live on webContents.
interface ContextParams {
  linkURL: string;
  selectionText: string;
  isEditable: boolean;
  // editFlags mirror document.queryCommandEnabled for the edit actions.
  editFlags: { canCut: boolean; canCopy: boolean; canPaste: boolean };
  canGoBack: boolean;
  canGoForward: boolean;
  // An ephemeral visit has nothing to re-descend into, so no Freeze Page.
  canFreeze: boolean;
}

// webviews.ts wires these to the clipboard and the view's webContents.
interface ContextActions {
  copyText(text: string): void;
  copyLink(url: string): void;
  openLink(url: string): void;
  cut(): void;
  paste(): void;
  back(): void;
  forward(): void;
  reload(): void;
  // Stores the standing frozen intent, so re-descending stays frozen until the
  // reconnect button clears it.
  freeze(): void;
}

// The subset of MenuItemConstructorOptions this builder emits, declared here so
// the module imports nothing from electron.
interface MenuTemplateItem {
  label?: string;
  type?: 'separator' | 'radio';
  checked?: boolean;
  enabled?: boolean;
  click?: () => void;
}

// Chromium's order: link, then text and edit, then navigation. Items that do
// not apply are omitted, except the navigation block, which disables instead.
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

// A menu of exclusive choices the renderer declared, one radio row each. It
// knows nothing about what is being chosen: the ids, labels and which one is
// checked all arrive on the wire, so a new kind of choice needs no change here.
export function choiceMenuTemplate(
  items: ChoiceItem[],
  choose: (id: string) => void,
): MenuTemplateItem[] {
  return items.map((it) => ({
    label: it.label,
    type: 'radio' as const,
    checked: it.checked,
    click: () => choose(it.id),
  }));
}
