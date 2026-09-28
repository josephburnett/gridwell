import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import type { BaseWindow, WebContentsView } from 'electron';
import { WebviewRegistry } from './webviews';
import type { ErrorEvent, ViewGoneEvent } from './ipc';

// The registry against a fake view, for the paths the capture harness cannot
// stage on demand: a webContents that ends under the registry.

// FakeWC is the slice of WebContents the registry touches. destroy() is what a
// page's window.close() does: 'destroyed', and then the view's webContents
// reads undefined.
class FakeWC extends EventEmitter {
  url = '';
  closed = 0;
  restoreRejects: ((err: Error) => void)[] = [];
  setWindowOpenHandler(): void {}
  setZoomFactor(): void {}
  loadURL(u: string): Promise<void> {
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
    return false;
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
  children: Set<FakeView>;
}

function rig(): Rig {
  const views: FakeView[] = [];
  const errors: ErrorEvent[] = [];
  const gone: ViewGoneEvent[] = [];
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
    { onError: (ev) => errors.push(ev), onViewGone: (ev) => gone.push(ev) },
    () => {
      const v = new FakeView();
      views.push(v);
      return v as unknown as WebContentsView;
    },
  );
  return { reg, views, errors, gone, children };
}

const BOUNDS = { x: 10, y: 20, width: 400, height: 300 };

test('a page that closes itself retires its entry: one notice, the renderer told, nothing throws', async () => {
  const r = rig();
  await r.reg.place('p1', 'u1/7', 'https://accounts.example/signinclose', BOUNDS);
  const view = r.views[0];

  view.destroy();

  assert.equal(r.reg.has('p1'), false, 'the entry outlived its view');
  assert.deepEqual(r.gone, [{ paneId: 'p1', tileId: 'u1/7' }], 'the renderer was not told the view ended');
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
