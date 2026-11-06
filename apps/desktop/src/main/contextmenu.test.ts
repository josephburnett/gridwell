import { test } from 'node:test';
import assert from 'node:assert/strict';
import { choiceMenuTemplate, urlContextMenuTemplate } from './contextmenu';

// The template's parameter shapes, read off the function. Nothing outside
// contextmenu.ts names them, so they are not exported.
type ContextParams = Parameters<typeof urlContextMenuTemplate>[0];
type ContextActions = Parameters<typeof urlContextMenuTemplate>[1];

// Each action records the argument it was called with, so a test can invoke an
// item's click() and assert the effect as the real menu does.
function spyActions() {
  const calls: Record<string, unknown[]> = {};
  const rec =
    (name: string) =>
    (...args: unknown[]) => {
      (calls[name] ??= []).push(args[0]);
    };
  const actions: ContextActions = {
    copyURL: rec('copyURL'),
    back: rec('back'),
    forward: rec('forward'),
    reload: rec('reload'),
    freeze: rec('freeze'),
    chose: rec('chose'),
  };
  return { actions, calls };
}

const NO_HISTORY: ContextParams = { canGoBack: false, canGoForward: false };

const labels = (t: ReturnType<typeof urlContextMenuTemplate>) =>
  t.filter((i) => i.label).map((i) => i.label);

// The pane's menu is Copy URL, the navigation block and Freeze Page, whatever
// the view holds.
test('the url menu is Copy URL, Back, Forward, Reload and Freeze Page', () => {
  const { actions } = spyActions();
  assert.deepEqual(labels(urlContextMenuTemplate(NO_HISTORY, actions)), ['Copy URL', 'Back', 'Forward', 'Reload', 'Freeze Page']);
});

test('Copy URL is always enabled and fires the action', () => {
  const { actions, calls } = spyActions();
  const item = urlContextMenuTemplate(NO_HISTORY, actions).find((i) => i.label === 'Copy URL')!;
  assert.equal(item.enabled, undefined);
  item.click!();
  assert.equal(calls.copyURL?.length, 1);
});

test('Back/Forward enablement tracks the history flags', () => {
  const { actions, calls } = spyActions();
  const t = urlContextMenuTemplate({ canGoBack: true, canGoForward: false }, actions);
  assert.equal(t.find((i) => i.label === 'Back')?.enabled, true);
  assert.equal(t.find((i) => i.label === 'Forward')?.enabled, false);

  t.find((i) => i.label === 'Back')!.click!();
  t.find((i) => i.label === 'Reload')!.click!();
  assert.equal(calls.back?.length, 1);
  assert.equal(calls.reload?.length, 1);
});

// Every live view offers Freeze Page, an ephemeral visit's like a placed
// tile's, and the click fires the injected action. Clearing browser state is
// the `gridwell clear-browser-data` CLI and no menu item.
test('Freeze Page appears on every live view and fires the action', () => {
  const { actions, calls } = spyActions();
  const t = urlContextMenuTemplate(NO_HISTORY, actions);
  const item = t.find((i) => i.label === 'Freeze Page');
  assert.ok(item, 'a live view offers Freeze Page');
  item!.click!();
  assert.equal((calls.freeze ?? []).length, 1);
  assert.ok(!t.some((i) => i.label?.startsWith('Clear Site Data')), 'clear site data is gone');
});

// The choice menu is a renderer declaration rendered verbatim: one radio row
// per item, in order, checked as declared, and the click hands the id straight
// back. A row it read for meaning would be a second owner of the choice.
test('choiceMenuTemplate renders declared choices verbatim and returns the id', () => {
  const picked: string[] = [];
  const items = choiceMenuTemplate(
    [
      { id: 'dark', label: 'Dark mode', checked: true },
      { id: 'light', label: 'Light mode', checked: false },
    ],
    (id) => picked.push(id),
  );
  assert.deepEqual(
    items.map((i) => [i.label, i.type, i.checked]),
    [
      ['Dark mode', 'radio', true],
      ['Light mode', 'radio', false],
    ],
  );
  items[1].click?.();
  assert.deepEqual(picked, ['light']);
});

test('choiceMenuTemplate on no choices is an empty menu, not a stray row', () => {
  assert.deepEqual(choiceMenuTemplate([], () => {}), []);
});

// A row that declares no state is an action. Drawn as a radio it reads as a
// setting that happens to be off, and the + menu's dump row is exactly that:
// see client/circlemenu.DumpID, which names no state.
test('a choice that declares no state is an action row, not an unchecked radio', () => {
  const picked: string[] = [];
  const items = choiceMenuTemplate(
    [
      { id: 'dark', label: 'Dark mode', checked: true },
      { id: 'dump', label: 'Dump logs' },
    ],
    (id) => picked.push(id),
  );
  assert.deepEqual(
    items.map((i) => [i.label, i.type, i.checked]),
    [
      ['Dark mode', 'radio', true],
      ['Dump logs', undefined, undefined],
    ],
  );
  items[1].click?.();
  assert.deepEqual(picked, ['dump'], 'the action row still hands its id back');
});

// Every row of the live view's menu reports the label it ran under, so one
// caller can say what the user picked and no row can be added that runs
// unreported.
test('every url menu row reports its own label before it acts', () => {
  const { actions, calls } = spyActions();
  const t = urlContextMenuTemplate({ canGoBack: true, canGoForward: true }, actions);
  const rows = t.filter((i) => i.label);
  for (const r of rows) r.click!();
  assert.deepEqual(calls.chose, rows.map((r) => r.label));
});
