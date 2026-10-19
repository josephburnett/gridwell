import { mock, test } from 'node:test';
import assert from 'node:assert/strict';
import {
  menuChose,
  menuOpened,
  viewBounds,
  viewCreated,
  viewDestroyed,
  viewFailed,
  viewFocused,
  viewGone,
  viewMoved,
  viewNav,
  viewShown,
  windowFocused,
  WINDOW_SETTLE_MS,
  windowState,
  windowStateRecorder,
} from './viewtrace';

// Every view record names its pane the same way, because a dump is read one
// pane at a time and a record that spells the key differently is invisible to
// that read.
test('every webview record carries the pane under one kv key', () => {
  const recs = [
    viewCreated('p1', 'u1/7', 'https://x.test/'),
    viewDestroyed('p1', 'u1/7', 'https://x.test/'),
    viewBounds('p1', 'u1/7', { x: 1, y: 2, width: 3, height: 4 }),
    viewShown('p1', 'u1/7', true),
    viewFocused('p1', 'u1/7', true),
    viewNav('p1', 'u1/7', false, 'https://x.test/'),
    viewFailed('p1', 'u1/7', 'load failed'),
    viewGone('p1', 'u1/7', 'https://x.test/'),
    viewMoved('p0', 'p1', 'u1/7'),
  ];
  for (const r of recs) {
    assert.equal(r.src, 'webview', `${r.kind} claims src ${r.src}`);
    assert.equal(r.kv?.view, 'p1', `${r.kind} does not name the pane`);
  }
});

// The geometry is kv, so a dump can be read as a table; the message says what
// the pane was showing, which no kv repeats.
test('a bounds record carries the rect as kv beside the tile', () => {
  const r = viewBounds('p1', 'u1/7', { x: 10, y: 20, width: 300, height: 400 });
  assert.equal(r.kind, 'bounds');
  assert.equal(r.msg, 'u1/7');
  assert.deepEqual(r.kv, { view: 'p1', x: '10', y: '20', w: '300', h: '400' });
});

// The paired states are one kind each, so a dump greps for the transition
// rather than for a flag inside a message.
test('the paired states read as their own kinds', () => {
  assert.equal(viewShown('p1', 'u1/7', true).kind, 'hide');
  assert.equal(viewShown('p1', 'u1/7', false).kind, 'show');
  assert.equal(viewFocused('p1', 'u1/7', true).kind, 'focus');
  assert.equal(viewFocused('p1', 'u1/7', false).kind, 'blur');
  assert.equal(viewNav('p1', 'u1/7', false, 'https://x.test/').kind, 'nav-start');
  assert.equal(viewNav('p1', 'u1/7', true, 'https://x.test/').kind, 'nav-done');
  assert.equal(windowFocused(true).kind, 'focus');
  assert.equal(windowFocused(false).kind, 'blur');
});

// The url is the whole point of a navigation record, so it is the message,
// where trace.ts truncates it rather than dropping the record.
test('a navigation record carries the address as its message', () => {
  const r = viewNav('p1', 'u1/7', true, 'https://x.test/a?b=1&c=2');
  assert.equal(r.msg, 'https://x.test/a?b=1&c=2');
  assert.equal(r.kv?.tile, 'u1/7');
});

// A menu that popped and a row that ran are two records, because a click that
// seems to have done nothing is exactly the gap between them.
test('a menu is one record for the pop and one for the row', () => {
  assert.deepEqual(menuOpened('url', 'p1'), {
    src: 'contextmenu',
    kind: 'open',
    msg: 'url',
    kv: { view: 'p1' },
  });
  assert.deepEqual(menuChose('url', 'Reload', 'p1'), {
    src: 'contextmenu',
    kind: 'choose',
    msg: 'Reload',
    kv: { menu: 'url', view: 'p1' },
  });
});

// The circle's declared choices pop over no pane, and a kv naming one would be
// a fact nobody owns.
test('a choice menu record names no pane', () => {
  assert.equal(menuOpened('choice').kv, undefined);
  assert.deepEqual(menuChose('choice', 'dump').kv, { menu: 'choice' });
});

const STATE = {
  resizable: true,
  maximized: false,
  fullScreen: false,
  minimizable: true,
  maximizable: true,
  bounds: { x: 10, y: 20, width: 1280, height: 800 },
  workArea: { width: 1920, height: 1040 },
  scaleFactor: 1.5,
  platform: 'win32',
  electron: '38.0.0',
  minSize: [0, 0] as [number, number],
  maxSize: [0, 0] as [number, number],
};

test('the window records name the window, not a pane', () => {
  assert.equal(windowFocused(true).src, 'window');
  assert.equal(windowState('resize', STATE).src, 'window');
  assert.equal(windowState('resize', STATE).kv?.view, undefined);
});

// One kind for every window event, the cause as the message, so a dump greps
// 'state' and reads the window's history as a table.
test('a window state record carries the cause as msg and the state as kv', () => {
  assert.deepEqual(windowState('unmaximize', STATE), {
    src: 'window',
    kind: 'state',
    msg: 'unmaximize',
    kv: {
      resizable: 'true',
      maximized: 'false',
      fullScreen: 'false',
      minimizable: 'true',
      maximizable: 'true',
      x: '10',
      y: '20',
      w: '1280',
      h: '800',
      workW: '1920',
      workH: '1040',
      scale: '1.5',
      platform: 'win32',
      electron: '38.0.0',
    },
  });
});

// A size limit is exactly what would stop an edge drag, so a set one shows.
test('a window state record names a size limit only when one is set', () => {
  const r = windowState('create', { ...STATE, resizable: false, minSize: [400, 300], maxSize: [1280, 0] });
  assert.equal(r.kv?.resizable, 'false');
  assert.equal(r.kv?.minSize, '400x300');
  assert.equal(r.kv?.maxSize, '1280x0');
});

// A drag is a burst of resize and move events; the ring keeps one record for
// it, the settled one, while a maximize is recorded as it fires.
test('a burst of resize and move records once, after it settles', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    const seen: string[] = [];
    const r = windowStateRecorder((cause) => seen.push(cause));
    r.now('create');
    r.settle('resize');
    r.settle('resize');
    r.settle('move');
    mock.timers.tick(WINDOW_SETTLE_MS - 1);
    assert.deepEqual(seen, ['create']);
    r.now('maximize');
    mock.timers.tick(1);
    assert.deepEqual(seen, ['create', 'maximize', 'move']);
    r.settle('resize');
    r.stop();
    mock.timers.tick(WINDOW_SETTLE_MS);
    assert.deepEqual(seen, ['create', 'maximize', 'move']);
  } finally {
    mock.timers.reset();
  }
});
