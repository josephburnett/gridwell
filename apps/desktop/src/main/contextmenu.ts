// Which items the two native menus offer. webviews.ts supplies the live url
// view's actions, register.ts the choice menu's; neither menu is built here.

import { ChoiceItem } from './ipc';

// The navigation flags, which live on webContents.
interface ContextParams {
  canGoBack: boolean;
  canGoForward: boolean;
}

// webviews.ts wires these to the view's webContents.
interface ContextActions {
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

// The live view's pane menu, which the bar circle pops. Back and Forward
// disable instead of vanishing.
export function urlContextMenuTemplate(p: ContextParams, a: ContextActions): MenuTemplateItem[] {
  // One wrapper, so no row can be added that runs without being reported.
  const row = (label: string, run: () => void, enabled?: boolean): MenuTemplateItem => ({
    label,
    ...(enabled === undefined ? {} : { enabled }),
    click: () => {
      a.chose(label);
      run();
    },
  });
  return [
    row('Back', () => a.back(), p.canGoBack),
    row('Forward', () => a.forward(), p.canGoForward),
    row('Reload', () => a.reload()),
    row('Freeze Page', () => a.freeze()),
  ];
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
