import { test } from 'node:test';
import assert from 'node:assert/strict';
import { SESSION_PARTITION, roundBounds, boundsEqual, controlVisible } from './viewutil';

test('SESSION_PARTITION is persistent and shared by all tiles', () => {
  // `persist:` prefix → durable on disk (logins/storage survive restarts).
  assert.ok(SESSION_PARTITION.startsWith('persist:'));
  // One partition for every tile: tiles act like tabs, sharing the session.
  // (There is no per-tile keying — that's the whole point of the change.)
});

test('roundBounds snaps to ints and floors size at 1', () => {
  assert.deepEqual(roundBounds({ x: 10.4, y: 20.6, width: 100.2, height: 50.9 }), {
    x: 10,
    y: 21,
    width: 100,
    height: 51,
  });
  assert.deepEqual(roundBounds({ x: 0, y: 0, width: 0, height: 0 }), {
    x: 0,
    y: 0,
    width: 1,
    height: 1,
  });
});

test('boundsEqual compares all four fields', () => {
  const a = { x: 1, y: 2, width: 3, height: 4 };
  assert.ok(boundsEqual(a, { ...a }));
  assert.ok(!boundsEqual(a, { ...a, x: 9 }));
  assert.ok(!boundsEqual(a, { ...a, height: 9 }));
});

test('controlVisible shows the corner circle only on the focused, unparked pane', () => {
  // The whole point of the bug fix: exactly one pane (the focused one) shows
  // its corner control at a time.
  assert.ok(controlVisible(false, true)); // focused, not parked → visible
  assert.ok(!controlVisible(false, false)); // unfocused → hidden (the bug)
  assert.ok(!controlVisible(true, true)); // focused but parked for a gesture → hidden
  assert.ok(!controlVisible(true, false)); // unfocused and parked → hidden
});
