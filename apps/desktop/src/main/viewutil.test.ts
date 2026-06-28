import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  SESSION_PARTITION,
  roundBounds,
  boundsEqual,
  controlVisible,
  dragExceeded,
  sanitizeUserAgent,
  RIGHT_DRAG_THRESHOLD,
} from './viewutil';

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

test('dragExceeded tells a right-click apart from a right-drag at the threshold', () => {
  const t = RIGHT_DRAG_THRESHOLD;
  // A still / barely-moved press is a click — passes through to the page menu.
  assert.ok(!dragExceeded(0, 0, t));
  assert.ok(!dragExceeded(t, 0, t)); // exactly threshold is still a click
  assert.ok(!dragExceeded(2, 2, t)); // 2.83px < 4
  // Past the threshold in any direction is a drag — arms the pane gesture.
  assert.ok(dragExceeded(t + 1, 0, t));
  assert.ok(dragExceeded(0, -(t + 1), t));
  assert.ok(dragExceeded(4, 4, t)); // 5.66px > 4
});

test('sanitizeUserAgent drops the Electron and app tokens, keeps Chrome', () => {
  const ua =
    'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) ' +
    'Gridwell/0.1.0 Chrome/120.0.0.0 Electron/28.0.0 Safari/537.36';
  const out = sanitizeUserAgent(ua, 'Gridwell');
  // The two embedding tokens that trip browser-version gates are gone…
  assert.ok(!/Electron\//.test(out));
  assert.ok(!/Gridwell\//.test(out));
  // …but the genuine engine tokens and platform group survive intact.
  assert.ok(out.includes('Chrome/120.0.0.0'));
  assert.ok(out.includes('Safari/537.36'));
  assert.ok(out.includes('(X11; Linux x86_64)'));
  assert.ok(out.includes('(KHTML, like Gecko)'));
  // No double spaces left where tokens were removed.
  assert.ok(!/ {2}/.test(out));
});

test('sanitizeUserAgent is idempotent and tolerates a missing app name', () => {
  const clean =
    'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) ' +
    'Chrome/120.0.0.0 Safari/537.36';
  // Already clean → unchanged (also covers re-running on our own output).
  assert.equal(sanitizeUserAgent(clean, 'Gridwell'), clean);
  // An empty app name still strips Electron without throwing on the regex.
  assert.ok(!/Electron\//.test(sanitizeUserAgent(`${clean} Electron/28.0.0`, '')));
});

test('controlVisible shows the corner circle only on the focused, unparked pane', () => {
  // The whole point of the bug fix: exactly one pane (the focused one) shows
  // its corner control at a time.
  assert.ok(controlVisible(false, true)); // focused, not parked → visible
  assert.ok(!controlVisible(false, false)); // unfocused → hidden (the bug)
  assert.ok(!controlVisible(true, true)); // focused but parked for a gesture → hidden
  assert.ok(!controlVisible(true, false)); // unfocused and parked → hidden
});
