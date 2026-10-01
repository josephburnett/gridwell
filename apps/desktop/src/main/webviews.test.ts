import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import type { BaseWindow, WebContentsView } from 'electron';
import { WebviewRegistry } from './webviews';
import type { ErrorEvent, NavEvent, ViewGoneEvent } from './ipc';

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
    return Promise.resolve({ isEmpty: () => true });
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
  children: Set<FakeView>;
}

function rig(): Rig {
  const views: FakeView[] = [];
  const errors: ErrorEvent[] = [];
  const gone: ViewGoneEvent[] = [];
  const navs: NavEvent[] = [];
  const steals: string[] = [];
  const children = new Set<FakeView>();
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
    },
    () => {
      const v = new FakeView();
      views.push(v);
      return v as unknown as WebContentsView;
    },
  );
  return { reg, views, errors, gone, navs, steals, children };
}

const BOUNDS = { x: 10, y: 20, width: 400, height: 300 };

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
  const freeze = await r.reg.remove('p1');
  assert.deepEqual(freeze, { jpegBase64: '', url: '', title: '', history: '' });
  assert.equal(r.errors.length, 1, `the calls after the end raised more notices: ${JSON.stringify(r.errors)}`);
  assert.equal(r.gone.length, 1);
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

test('a park still taking its face for the old pane does not park the view it moved', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/9', SLACK, BOUNDS);
  const view = r.views[0];
  r.reg.setHidden('p1', true, false); // waits on its face before it parks
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
