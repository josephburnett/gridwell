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
  // Every row reports the label it ran under, so one caller can say what the
  // user picked without the builder knowing what any item means.
  chose(label: string): void;
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
  // One wrapper, so no row can be added that runs without being reported.
  const row = (label: string, run: () => void, enabled?: boolean): MenuTemplateItem => ({
    label,
    ...(enabled === undefined ? {} : { enabled }),
    click: () => {
      a.chose(label);
      run();
    },
  });

  if (p.linkURL) {
    items.push(row('Open Link', () => a.openLink(p.linkURL)));
    items.push(row('Copy Link Address', () => a.copyLink(p.linkURL)));
    items.push({ type: 'separator' });
  }

  if (p.isEditable) {
    items.push(row('Cut', () => a.cut(), p.editFlags.canCut));
    items.push(row('Copy', () => a.copyText(p.selectionText), p.editFlags.canCopy));
    items.push(row('Paste', () => a.paste(), p.editFlags.canPaste));
    items.push({ type: 'separator' });
  } else if (p.selectionText) {
    items.push(row('Copy', () => a.copyText(p.selectionText)));
    items.push({ type: 'separator' });
  }

  items.push(row('Back', () => a.back(), p.canGoBack));
  items.push(row('Forward', () => a.forward(), p.canGoForward));
  items.push(row('Reload', () => a.reload()));
  if (p.canFreeze) {
    items.push(row('Freeze Page', () => a.freeze()));
  }

  return items;
}

// A menu of choices the renderer declared. It knows nothing about what is being
// chosen: the ids, labels and state all arrive on the wire, so a new kind of
// choice needs no change here. A row that declares state is a radio in the
// group; a row that declares none is an action, and an action drawn as an
// unchecked radio reads as a setting that is off.
export function choiceMenuTemplate(
  items: ChoiceItem[],
  choose: (id: string) => void,
): MenuTemplateItem[] {
  return items.map((it) =>
    it.checked === undefined
      ? { label: it.label, click: () => choose(it.id) }
      : { label: it.label, type: 'radio' as const, checked: it.checked, click: () => choose(it.id) },
  );
}
