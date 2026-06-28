import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  SESSION_PARTITION,
  roundBounds,
  boundsEqual,
  controlVisible,
  controlBounds,
  parkedBounds,
  minWidthZoomFactor,
  PARK_COORD,
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

test('controlBounds sits the corner control inside the view bottom-right', () => {
  // A 200x100 view at (10,20) with a 36px control inset 6px: the control's
  // far edge lines up with the view's far edge minus the margin.
  const b = controlBounds({ x: 10, y: 20, width: 200, height: 100 }, 36, 6);
  assert.equal(b.width, 36);
  assert.equal(b.height, 36);
  assert.equal(b.x, 10 + 200 - 36 - 6); // 168
  assert.equal(b.y, 20 + 100 - 36 - 6); // 78
  // It stays within the view's content box on both axes.
  assert.ok(b.x + b.width <= 10 + 200);
  assert.ok(b.y + b.height <= 20 + 100);
});

test('parkedBounds moves a view far off-screen but keeps its size', () => {
  const p = parkedBounds(200, 100);
  assert.equal(p.x, PARK_COORD);
  assert.equal(p.y, PARK_COORD);
  assert.ok(p.x < -1000 && p.y < -1000); // genuinely off any display
  assert.equal(p.width, 200); // size preserved → un-parking is a pure move
  assert.equal(p.height, 100);
});

test('minWidthZoomFactor scales a narrow view to fit and clamps the floor', () => {
  const min = 640;
  assert.equal(minWidthZoomFactor(640, min), 1); // at the threshold → no scaling
  assert.equal(minWidthZoomFactor(800, min), 1); // wider → no scaling
  assert.equal(minWidthZoomFactor(320, min), 0.5); // half width → half zoom
  // Below the floor the zoom clamps at 0.25 rather than shrinking to nothing.
  assert.equal(minWidthZoomFactor(64, min), 0.25);
  assert.equal(minWidthZoomFactor(1, min), 0.25);
});
