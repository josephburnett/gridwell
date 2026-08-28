import { test } from 'node:test';
import assert from 'node:assert/strict';
import { urlContextMenuTemplate } from './contextmenu';

// The template's parameter shapes, read off the function: nothing outside
// contextmenu.ts names them, so they are not exported.
type ContextParams = Parameters<typeof urlContextMenuTemplate>[0];
type ContextActions = Parameters<typeof urlContextMenuTemplate>[1];

// A spy bag: each action records the argument it was called with so a test can
// invoke an item's click() and assert the effect, exactly as the real menu does.
function spyActions() {
  const calls: Record<string, unknown[]> = {};
  const rec =
    (name: string) =>
    (...args: unknown[]) => {
      (calls[name] ??= []).push(args[0]);
    };
  const actions: ContextActions = {
    copyText: rec('copyText'),
    copyLink: rec('copyLink'),
    openLink: rec('openLink'),
    cut: rec('cut'),
    paste: rec('paste'),
    back: rec('back'),
    forward: rec('forward'),
    reload: rec('reload'),
    freeze: rec('freeze'),
  };
  return { actions, calls };
}

function baseParams(over: Partial<ContextParams> = {}): ContextParams {
  return {
    linkURL: '',
    selectionText: '',
    isEditable: false,
    editFlags: { canCut: false, canCopy: false, canPaste: false },
    canGoBack: false,
    canGoForward: false,
    canFreeze: false,
    ...over,
  };
}

const labels = (t: ReturnType<typeof urlContextMenuTemplate>) =>
  t.filter((i) => i.label).map((i) => i.label);

// The navigation block is always present (it disables rather than vanishes),
// so every menu ends with Back / Forward / Reload.
test('navigation items are always present', () => {
  const { actions } = spyActions();
  const t = urlContextMenuTemplate(baseParams(), actions);
  assert.deepEqual(labels(t), ['Back', 'Forward', 'Reload']);
});

// THE REGRESSION GUARD for the reported bug: a right-click over a link must
// offer "Copy Link Address", and clicking it must copy that exact URL.
test('a link yields Open Link + Copy Link Address that copy the href', () => {
  const { actions, calls } = spyActions();
  const url = 'https://example.com/target?x=1';
  const t = urlContextMenuTemplate(baseParams({ linkURL: url }), actions);

  assert.deepEqual(labels(t), ['Open Link', 'Copy Link Address', 'Back', 'Forward', 'Reload']);

  const copy = t.find((i) => i.label === 'Copy Link Address');
  assert.ok(copy?.click, 'Copy Link Address must have a click handler');
  copy!.click!();
  assert.deepEqual(calls.copyLink, [url]);

  const open = t.find((i) => i.label === 'Open Link');
  open!.click!();
  assert.deepEqual(calls.openLink, [url]);
});

// No link → no link items (the menu never shows a dead "Copy Link Address").
test('no link omits the link items', () => {
  const { actions } = spyActions();
  const t = urlContextMenuTemplate(baseParams(), actions);
  assert.ok(!labels(t).includes('Copy Link Address'));
  assert.ok(!labels(t).includes('Open Link'));
});

// A text selection over non-editable content offers Copy of that text.
test('selection over non-editable content offers Copy of the selection', () => {
  const { actions, calls } = spyActions();
  const t = urlContextMenuTemplate(baseParams({ selectionText: 'hello world' }), actions);
  const copy = t.find((i) => i.label === 'Copy');
  assert.ok(copy, 'Copy should be present for a selection');
  copy!.click!();
  assert.deepEqual(calls.copyText, ['hello world']);
});

// An editable field offers Cut / Copy / Paste, each enabled by its editFlag.
test('editable content offers Cut/Copy/Paste gated by editFlags', () => {
  const { actions, calls } = spyActions();
  const t = urlContextMenuTemplate(
    baseParams({
      isEditable: true,
      selectionText: 'sel',
      editFlags: { canCut: true, canCopy: true, canPaste: false },
    }),
    actions,
  );
  const cut = t.find((i) => i.label === 'Cut');
  const copy = t.find((i) => i.label === 'Copy');
  const paste = t.find((i) => i.label === 'Paste');
  assert.equal(cut?.enabled, true);
  assert.equal(copy?.enabled, true);
  assert.equal(paste?.enabled, false, 'Paste disabled when canPaste is false');

  cut!.click!();
  paste!.click!();
  copy!.click!();
  assert.equal(calls.cut?.length, 1);
  assert.equal(calls.paste?.length, 1, 'a disabled item still has its handler; enablement is enforced by Electron');
  assert.deepEqual(calls.copyText, ['sel']);
});

// Navigation enablement follows canGoBack / canGoForward; the actions fire.
test('Back/Forward enablement tracks the history flags', () => {
  const { actions, calls } = spyActions();
  const t = urlContextMenuTemplate(baseParams({ canGoBack: true, canGoForward: false }), actions);
  assert.equal(t.find((i) => i.label === 'Back')?.enabled, true);
  assert.equal(t.find((i) => i.label === 'Forward')?.enabled, false);

  t.find((i) => i.label === 'Back')!.click!();
  t.find((i) => i.label === 'Reload')!.click!();
  assert.equal(calls.back?.length, 1);
  assert.equal(calls.reload?.length, 1);
});

// The explicit freeze gesture (issue #237), gated by durability (issue
// #240): a DURABLE tile's menu offers Freeze Page and its click fires the
// injected action; an ephemeral visit — nothing to re-descend into — gets
// no item at all. (Clear Site Data is gone entirely: clearing browser
// state is the `gridwell clear-browser-data` CLI now.)
test('Freeze Page appears only for a durable tile and fires the action', () => {
  const { actions, calls } = spyActions();
  const t = urlContextMenuTemplate(baseParams({ canFreeze: true }), actions);
  const item = t.find((i) => i.label === 'Freeze Page');
  assert.ok(item, 'a durable tile offers Freeze Page');
  item!.click!();
  assert.equal((calls.freeze ?? []).length, 1);

  const eph = urlContextMenuTemplate(baseParams(), actions);
  assert.ok(!labels(eph).includes('Freeze Page'), 'an ephemeral visit offers no freeze');
  assert.ok(!eph.some((i) => i.label?.startsWith('Clear Site Data')), 'clear site data is gone');
});
