import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  viewBounds,
  viewCreated,
  viewDestroyed,
  viewFailed,
  viewFocused,
  viewNav,
  viewShown,
  windowFocused,
  windowResized,
} from './viewtrace';

// Every view record names its pane the same way, because a dump is read one
// pane at a time and a record that spells the key differently is invisible to
// that read.
test('every webviews record carries the pane under one kv key', () => {
  const recs = [
    viewCreated('p1', 'u1/7', 'https://x.test/'),
    viewDestroyed('p1', 'u1/7', 'https://x.test/'),
    viewBounds('p1', 'u1/7', { x: 1, y: 2, width: 3, height: 4 }),
    viewShown('p1', 'u1/7', true),
    viewFocused('p1', 'u1/7', true),
    viewNav('p1', 'u1/7', false, 'https://x.test/'),
    viewFailed('p1', 'u1/7', 'load failed'),
  ];
  for (const r of recs) {
    assert.equal(r.src, 'webviews', `${r.kind} claims src ${r.src}`);
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

test('the window records name the window, not a pane', () => {
  assert.equal(windowFocused(true).src, 'window');
  assert.equal(windowResized(1280, 800).msg, '1280x800');
  assert.equal(windowResized(1280, 800).kv, undefined);
});
