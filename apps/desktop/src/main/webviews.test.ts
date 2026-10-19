import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import type { BaseWindow, WebContentsView } from 'electron';
import { WebviewRegistry } from './webviews';
import { viewCreated } from './viewtrace';
import { FOCUS_SETTLE_MS } from './focusguard';
import type { ErrorEvent, NavEvent, ViewGoneEvent } from './ipc';
import type { TraceEvent } from './trace';

// The registry against a fake view, for the paths the capture harness cannot
// stage on demand: a webContents that ends under the registry, a restore raced
// by a remove, and a view handed between panes.

// FakeWC is the slice of WebContents the registry touches. destroy() is what a
// page's window.close() does: 'destroyed', and then the view's webContents
// reads undefined.
class FakeWC extends EventEmitter {
  url = '';
  loads = 0;
  focused = false;
  closed = 0;
  // What capturePage answers; null is an empty frame.
  frame: Buffer | null = null;
  restoreRejects: ((err: Error) => void)[] = [];
  setWindowOpenHandler(): void {}
  setZoomFactor(): void {}
  loadURL(u: string): Promise<void> {
    this.loads++;
    this.url = u;
    return Promise.resolve();
  }
  getURL(): string {
    return this.url;
  }
  getTitle(): string {
    return 'title';
  }
  isFocused(): boolean {
    return this.focused;
  }
  capturePage(): Promise<unknown> {
    const frame = this.frame;
    return Promise.resolve({ isEmpty: () => !frame, toJPEG: () => frame });
  }
  close(): void {
    this.closed++;
  }
  navigationHistory = {
    restore: (): Promise<void> =>
      new Promise((_resolve, reject) => {
        this.restoreRejects.push(reject);
      }),
    getAllEntries: () => [],
    getActiveIndex: () => 0,
    canGoBack: () => false,
    canGoForward: () => false,
  };
}

class FakeView {
  webContents: FakeWC | undefined = new FakeWC();
  bounds = { x: 0, y: 0, width: 0, height: 0 };
  setBounds(b: { x: number; y: number; width: number; height: number }): void {
    this.bounds = b;
  }
  getBounds() {
    return this.bounds;
  }
  destroy(): void {
    const wc = this.webContents!;
    wc.emit('destroyed');
    this.webContents = undefined;
  }
}

interface Rig {
  reg: WebviewRegistry;
  views: FakeView[];
  errors: ErrorEvent[];
  gone: ViewGoneEvent[];
  navs: NavEvent[];
  steals: string[];
  escapes: string[];
  children: Set<FakeView>;
  records: TraceEvent[];
}

function rig(): Rig {
  const views: FakeView[] = [];
  const errors: ErrorEvent[] = [];
  const gone: ViewGoneEvent[] = [];
  const navs: NavEvent[] = [];
  const steals: string[] = [];
  const escapes: string[] = [];
  const children = new Set<FakeView>();
  const records: TraceEvent[] = [];
  const win = {
    contentView: {
      addChildView: (v: FakeView) => children.add(v),
      removeChildView: (v: FakeView) => children.delete(v),
    },
    setFullScreen: () => {},
    isFullScreen: () => false,
    getContentBounds: () => ({ x: 0, y: 0, width: 800, height: 600 }),
  } as unknown as BaseWindow;
  const reg = new WebviewRegistry(
    win,
    {
      onError: (ev) => errors.push(ev),
      onViewGone: (ev) => gone.push(ev),
      onNav: (ev) => navs.push(ev),
      onFocusStolen: (ev) => steals.push(ev.paneId),
      onEscapeKey: (ev) => escapes.push(ev.paneId),
    },
    () => {
      const v = new FakeView();
      views.push(v);
      return v as unknown as WebContentsView;
    },
    (ev) => records.push(ev),
  );
  return { reg, views, errors, gone, navs, steals, escapes, children, records };
}

const BOUNDS = { x: 10, y: 20, width: 400, height: 300 };

function kinds(r: Rig, kind: string): TraceEvent[] {
  return r.records.filter((ev) => ev.kind === kind);
}

test('a page that closes itself retires its entry: one notice, the renderer told, nothing throws', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://accounts.example/signinclose', BOUNDS, 0, '', false, false, false, 3);
  const view = r.views[0];

  view.destroy();

  assert.equal(r.reg.has('p1'), false, 'the entry outlived its view');
  assert.deepEqual(r.gone, [{ paneId: 'p1', tileId: 'u1/7', gen: 3 }], 'the renderer was not told the view ended');
  assert.equal(r.children.has(view), false, 'the dead view is still attached over the pane');
  assert.equal(r.errors.length, 1, `want one notice, got ${JSON.stringify(r.errors)}`);
  assert.match(r.errors[0].message, /closed itself/);
  assert.equal(r.errors[0].severity, 'info');

  // Everything the renderer and the mirror pump still send for the pane.
  assert.equal(await r.reg.capture('p1'), '');
  await r.reg.mirror('p1');
  r.reg.setBounds('p1', { ...BOUNDS, width: 500 });
  r.reg.setHidden('p1', true, false);
  r.reg.setZoom('p1', 1.5);
  r.reg.goBack('p1');
  r.reg.reload('p1', 'https://accounts.example/signinclose');
  const freeze = await r.reg.remove('p1');
  assert.deepEqual(freeze, { jpegBase64: '', url: '', title: '', history: '' });
  assert.equal(r.errors.length, 1, `the calls after the end raised more notices: ${JSON.stringify(r.errors)}`);
  assert.equal(r.gone.length, 1);
});

test('a view whose renderer died is retired like one that closed: one notice, the renderer told, the page closed', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://example.com/', BOUNDS, 0, '', true, false, true, 4);
  const view = r.views[0];
  const wc = view.webContents!;
  // Closing a crashed webContents destroys it, which must not announce twice.
  wc.close = () => {
    wc.closed++;
    view.destroy();
  };

  wc.emit('render-process-gone', {}, { reason: 'crashed', exitCode: 133 });

  assert.equal(r.reg.has('p1'), false, 'the entry outlived its renderer');
  assert.deepEqual(r.gone, [{ paneId: 'p1', tileId: 'u1/7', gen: 4 }], 'the renderer was not told the view ended');
  assert.equal(r.children.has(view), false, 'the dead view is still attached over the pane');
  assert.equal(wc.closed, 1, 'the crashed webContents was left open');
  assert.deepEqual(
    r.errors.map((e) => `${e.severity}: ${e.message}`),
    ['error: page crashed (crashed): https://example.com/'],
  );
  const freeze = await r.reg.remove('p1');
  assert.deepEqual(freeze, { jpegBase64: '', url: '', title: '', history: '' });
  assert.equal(r.gone.length, 1);
  assert.equal(r.errors.length, 1);
});

test('a crash notice names the page from the address main holds, not the dead renderer', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://example.com/', BOUNDS);
  const wc = r.views[0].webContents!;
  wc.url = 'https://example.com/second';
  wc.emit('did-navigate');
  // A crashed renderer answers getURL() with nothing (seen on CI).
  wc.url = '';
  wc.emit('render-process-gone', {}, { reason: 'crashed', exitCode: 133 });
  assert.deepEqual(
    r.errors.map((e) => `${e.severity}: ${e.message}`),
    ['error: page crashed (crashed): https://example.com/second'],
  );
});

test('a renderer that dies under remove() is remove()\'s to report', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://example.com/', BOUNDS);
  const wc = r.views[0].webContents!;
  wc.close = () => {
    wc.emit('render-process-gone', {}, { reason: 'killed', exitCode: 9 });
  };
  await r.reg.remove('p1');
  assert.deepEqual(r.gone, []);
  assert.deepEqual(r.errors, []);
});

test('the registry closing a view itself is not a page that closed itself', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://example.com/', BOUNDS);
  const view = r.views[0];
  const wc = view.webContents!;
  wc.close = () => view.destroy();

  await r.reg.remove('p1');

  assert.deepEqual(r.gone, [], 'remove() announced its own close as a view gone');
  assert.deepEqual(r.errors, []);
});

// A back-stack two entries deep whose active entry is the address, so place()
// restores it rather than loading.
const SLACK = 'https://app.slack.com/client';
const STACK = JSON.stringify({ index: 1, entries: [{ url: 'https://app.slack.com/', title: 'a' }, { url: SLACK, title: 'b' }] });
const settle = () => new Promise((r) => setImmediate(r));

test('a restore cut short by the view going away is not a refused back-stack', async () => {
  const r = rig();
  await r.reg.place('w1:p327', 'u1/9', SLACK, BOUNDS, 0, STACK);
  const wc = r.views[0].webContents!;
  assert.equal(wc.restoreRejects.length, 1, 'place did not restore the stack');

  // The takeover shape: the view is removed while its restore is in flight.
  await r.reg.remove('w1:p327');
  wc.restoreRejects[0](new Error(`ERR_FAILED (-2) loading '${SLACK}'`));
  await settle();

  assert.deepEqual(r.errors, [], 'a restore the registry cut short was reported as refused');
});

test('a live view whose stack Chromium refuses is still reported', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, STACK);
  r.views[0].webContents!.restoreRejects[0](new Error(`ERR_FAILED (-2) loading '${SLACK}'`));
  await settle();

  assert.equal(r.errors.length, 1);
  assert.match(r.errors[0].message, /^pane p1: stored back-stack refused \(ERR_FAILED/);
});

test('a move re-keys the view onto the new pane: its bounds, no reload, and every event under the new id', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, '', true, false, true);
  const view = r.views[0];
  const wc = view.webContents!;

  r.reg.move('p1', 'w1:p1', { x: 100.4, y: 50, width: 600, height: 400 }, true, false, true);

  assert.equal(r.views.length, 1, 'a move made a second view');
  assert.equal(wc.loads, 1, 'a move reloaded the page');
  assert.equal(wc.closed, 0, 'a move closed the page');
  assert.ok(r.children.has(view));
  assert.deepEqual(r.reg.paneIds(), ['w1:p1']);
  assert.equal(r.reg.tileIdFor('w1:p1'), 'u1/9');
  assert.deepEqual(view.bounds, { x: 100, y: 50, width: 600, height: 400 });

  // The old id answers nothing, so a mirror or a bounds meant for it is a no-op.
  assert.equal(r.reg.has('p1'), false);
  r.reg.setBounds('p1', BOUNDS);
  assert.deepEqual(view.bounds, { x: 100, y: 50, width: 600, height: 400 }, 'the old pane still moved the view');
  assert.equal(await r.reg.capture('p1'), '');

  // What the page does next is the new pane's.
  wc.url = 'https://app.slack.com/client/C1';
  wc.emit('did-navigate');
  assert.equal(r.navs.at(-1)?.paneId, 'w1:p1');
  assert.deepEqual(r.errors, []);

  const freeze = await r.reg.remove('w1:p1');
  assert.equal(freeze.url, 'https://app.slack.com/client/C1');
  assert.equal(wc.closed, 1);
});

test('a move into a hidden pane parks the view at the size it had, and one with no bounds keeps them', async () => {
  const r = rig();
  await r.reg.place('w1:p1', 'u1/9', SLACK, BOUNDS);
  const view = r.views[0];

  r.reg.move('w1:p1', 'p1', undefined, true, true, false);

  assert.ok(view.bounds.x < -1000, 'a hidden move left the view on screen');
  assert.equal(view.bounds.width, BOUNDS.width, 'a move with no bounds resized the page');
  r.reg.setHidden('p1', false, true);
  assert.deepEqual(view.bounds, BOUNDS, 'the next show put it back where it was');
});

test('a park still taking its preview for the old pane does not park the view it moved', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS);
  const view = r.views[0];
  r.reg.setHidden('p1', true, false); // waits on its preview before it parks
  r.reg.move('p1', 'w1:p1', BOUNDS, true, false, true);
  await settle();
  assert.deepEqual(view.bounds, BOUNDS, 'the stale park moved the view off screen');
});

test('a view that holds OS focus and moves to an unfocused pane gives the focus back', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, '', true, false, true);
  r.views[0].webContents!.focused = true;
  r.reg.move('p1', 'w1:p1', BOUNDS, true, false, false);
  assert.deepEqual(r.steals, ['w1:p1']);
  assert.deepEqual(kinds(r, 'steal').map((ev) => ev.kv), [{ view: 'w1:p1', phase: 'move' }]);
});

// A keystroke that went to the canvas instead of the page reads in a dump as a
// steal the guard bounced.
test('a focus grab on an unfocused pane is bounced and traced with its phase', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, '', true, false, false);
  const wc = r.views[0].webContents!;
  wc.focused = true;
  wc.emit('focus');
  await new Promise((res) => setTimeout(res, FOCUS_SETTLE_MS + 30));
  await r.reg.remove('p1'); // cancels the confirmation
  assert.deepEqual(r.steals, ['p1']);
  assert.deepEqual(kinds(r, 'steal').map((ev) => ev.kv), [{ view: 'p1', phase: 'settle' }]);
});

// A key event as before-input-event hands it, with the prevented flag a
// relay sets.
function keyDown(wc: FakeWC, key: string): boolean {
  let prevented = false;
  wc.emit('before-input-event', { preventDefault: () => (prevented = true) }, { type: 'keyDown', key });
  return prevented;
}

test('a parked view hands Esc to the renderer; a shown one keeps it for its page', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, '', true, false, true);
  const wc = r.views[0].webContents!;

  assert.equal(keyDown(wc, 'Escape'), false, 'a shown view gave its page\'s Esc away');
  assert.deepEqual(r.escapes, []);

  r.reg.setHidden('p1', true, true);
  assert.equal(keyDown(wc, 'Escape'), true, 'a parked view let its offscreen page take Esc');
  assert.equal(keyDown(wc, 'a'), false, 'a parked view took a key that is not Esc');
  assert.deepEqual(r.escapes, ['p1']);
  assert.equal(r.reg.escapeRelays, 1);
});

test('a reload loads the address again in the same view, and a pane with no view ignores it', async () => {
  const r = rig();
  const page = 'http://127.0.0.1:1/content/tok/p%2F~dA/';
  await r.reg.place('p1', 'p/~dA', page, BOUNDS, 0, '', true, false, true);
  const wc = r.views[0].webContents!;

  r.reg.reload('p1', page);
  r.reg.reload('p2', page);

  assert.equal(r.views.length, 1, 'a reload made a second view');
  assert.equal(wc.loads, 2, 'the page did not load again');
  assert.equal(wc.url, page);
  assert.equal(wc.closed, 0, 'a reload closed the page');
  assert.deepEqual(r.errors, []);
});

test('a move from a pane that holds no view is refused', async () => {
  const r = rig();
  assert.throws(() => r.reg.move('p1', 'w1:p1', BOUNDS, true, false, true), /no live view to move/);
});

test('a restore on a moved view belongs to the view: a refusal reports under the new pane', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, STACK);
  r.reg.move('p1', 'w1:p1', BOUNDS, true, false, true);
  r.views[0].webContents!.restoreRejects[0](new Error(`ERR_FAILED (-2) loading '${SLACK}'`));
  await settle();
  assert.equal(r.errors.length, 1);
  assert.match(r.errors[0].message, /^pane w1:p1: stored back-stack refused/);
});

test('a gone event names the view by the gen it was placed with, which a move keeps', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, '', true, false, true, 1);
  const first = r.views[0];
  await r.reg.remove('p1');
  // The same tile live again in the same pane: only the gen tells the views apart.
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, '', true, false, true, 2);
  r.reg.move('p1', 'w1:p1', BOUNDS, true, false, true);

  first.webContents!.emit('destroyed');
  assert.deepEqual(r.gone, [], 'the removed view announced an end');

  r.views[1].destroy();
  assert.deepEqual(r.gone, [{ paneId: 'w1:p1', tileId: 'u1/9', gen: 2 }]);
});

test('a mirror that stops capturing is reported once, and its recovery once', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS);
  const wc = r.views[0].webContents!;
  const frame = { isEmpty: () => false, toJPEG: () => Buffer.from('jpeg') };
  wc.capturePage = () => Promise.resolve(frame);
  assert.notEqual(await r.reg.capture('p1'), '');

  wc.capturePage = () => Promise.reject(new Error('GPU process gone'));
  for (let i = 0; i < 3; i++) assert.equal(await r.reg.capture('p1'), '');
  wc.capturePage = () => Promise.resolve(frame);
  assert.notEqual(await r.reg.capture('p1'), '');
  assert.notEqual(await r.reg.capture('p1'), '');

  assert.deepEqual(
    r.errors.map((e) => `${e.severity}: ${e.message}`),
    [
      'error: pane p1: mirror capture failing: capturePage failed: Error: GPU process gone',
      'info: pane p1: mirror capture recovered after 3 failed captures',
    ],
  );
});

// The registry is one owner, so its notices and its trace records name it with
// one word: the notice row's source is the record's origin and src.
test('a registry notice names the owner its trace records do', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://x.test/', BOUNDS);
  await r.reg.place('p1', 'u1/8', 'https://y.test/', BOUNDS);
  assert.equal(r.errors.length, 1, `want the replace notice, got ${JSON.stringify(r.errors)}`);
  assert.equal(r.errors[0].source, `electron:${viewCreated('p1', 'u1/7', '').src}`);
});

// A dump that shows a view gone has to say whether its page closed itself or
// its renderer died, because the two are different bugs.
test('a gone record says why the view ended', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://x.test/', BOUNDS);
  r.views[0].webContents!.emit('render-process-gone', {}, { reason: 'oom', exitCode: 9 });
  await r.reg.place('p2', 'u1/8', 'https://y.test/', BOUNDS);
  r.views[1].destroy();
  assert.deepEqual(
    kinds(r, 'gone').map((ev) => [ev.kv?.view, ev.kv?.reason]),
    [['p1', 'oom'], ['p2', 'closed']],
  );
});

// A tile whose preview did not update after an ascent is read from whether the
// close took a frame.
test('a destroy record says how big a preview the close took', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://x.test/', BOUNDS);
  r.views[0].webContents!.frame = Buffer.from('jpegbytes');
  await r.reg.remove('p1');
  await r.reg.place('p2', 'u1/8', 'https://y.test/', BOUNDS);
  await r.reg.remove('p2');
  assert.deepEqual(
    kinds(r, 'destroy').map((ev) => [ev.kv?.view, ev.kv?.frame]),
    [['p1', '9'], ['p2', '0']],
  );
});

// A pane showing a page other than the one it was left on is read from whether
// the view restored its back-stack or loaded the address.
test('a new view says whether it restored its back-stack or loaded the address', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS, 0, STACK);
  await r.reg.place('p2', 'u1/8', 'https://y.test/', BOUNDS);
  assert.deepEqual(
    kinds(r, 'revive').map((ev) => [ev.kv?.view, ev.kv?.how, ev.msg]),
    [['p1', 'restore', SLACK], ['p2', 'load', 'https://y.test/']],
  );
});

// An address change inside a page fires no load, so without its own record a
// dump shows the view on the address it loaded.
test('an in-page navigation that moves the main frame address is traced once', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', 'https://x.test/', BOUNDS);
  const wc = r.views[0].webContents!;
  wc.url = 'https://x.test/#b';
  wc.emit('did-navigate-in-page', {}, 'https://x.test/#b', true);
  wc.emit('did-navigate-in-page', {}, 'https://x.test/#b', true);
  wc.emit('did-navigate-in-page', {}, 'https://frame.test/#c', false);
  assert.deepEqual(
    kinds(r, 'nav-page').map((ev) => [ev.kv?.view, ev.msg]),
    [['p1', 'https://x.test/#b']],
  );
  assert.equal(r.navs.length, 3, 'the renderer still hears every in-page event');
});
