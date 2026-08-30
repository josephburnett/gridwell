import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  SESSION_PARTITION,
  roundBounds,
  boundsEqual,
  parkedBounds,
  zoomChordKey,
  minWidthZoomFactor,
  composeZoom,
  allowPermission,
  openBelowUrl,
  serializeHistory,
  parseHistory,
  reviveNavigation,
  URL_MIN_LAYOUT_WIDTH,
  PARK_COORD,
  classifyRightPress,
  sanitizeUserAgent,
  shouldSurfaceFailLoad,
  failLoadMessage,
  renderProcessGoneMessage,
  rendererLogLine,
} from './viewutil';

test('SESSION_PARTITION is persistent and shared by all tiles', () => {
  // `persist:` prefix → durable on disk (logins/storage survive restarts).
  assert.ok(SESSION_PARTITION.startsWith('persist:'));
  // One partition for every tile: tiles act like tabs, sharing the session.
  // There is no per-tile keying.
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

test('composeZoom multiplies layout and user zoom, clamped to the Chromium floor', () => {
  assert.equal(composeZoom(1, 1.5), 1.5); // wide pane, user zoomed in
  assert.equal(composeZoom(0.5, 2), 1); // they multiply
  assert.equal(composeZoom(1, 0), 1); // 0 = unset → 1.0
  assert.equal(composeZoom(0.25, 0.5), 0.25); // floor
});

test('URL_MIN_LAYOUT_WIDTH pins the production zoom-to-fit threshold', () => {
  // Below 800 the page keeps an 800px desktop layout scaled to fit, instead of
  // reflowing to a cramped semi-mobile layout.
  assert.equal(URL_MIN_LAYOUT_WIDTH, 800);
  assert.equal(minWidthZoomFactor(900, URL_MIN_LAYOUT_WIDTH), 1);
  assert.equal(minWidthZoomFactor(400, URL_MIN_LAYOUT_WIDTH), 0.5);
});

// A fast trackpad tap that drifts a few pixels past the 4px threshold is still
// a click, so the context menu fires. A fast flick past the far threshold is a
// gesture regardless of duration, and a hold-and-drag exceeding both small
// thresholds is a gesture too.
test('classifyRightPress requires both distance and time to classify as drag', () => {
  const dist = 4; // px — viewutil's RIGHT_DRAG_THRESHOLD (drift-linted against the canvas)
  const time = 200; // ms — viewutil's RIGHT_DRAG_TIME_MS

  // Neither condition met: a click.
  assert.ok(!classifyRightPress(0, 0, 0, dist, time), 'zero movement, zero time → click');

  // Distance exceeded but the button released fast (trackpad jitter): a click.
  assert.ok(
    !classifyRightPress(dist + 1, 0, time - 1, dist, time),
    'distance exceeded but duration < threshold → click (jitter case)',
  );
  assert.ok(
    !classifyRightPress(0, dist + 1, time - 1, dist, time),
    'y-only distance exceeded, short hold → click',
  );

  // Time exceeded but barely any movement: a click, the user just held.
  assert.ok(
    !classifyRightPress(0, 0, time + 100, dist, time),
    'long hold but no movement → click',
  );

  // Both conditions met: a drag, the intentional pane gesture.
  assert.ok(
    classifyRightPress(dist + 1, 0, time, dist, time),
    'distance and time both at/above threshold → drag',
  );
  assert.ok(
    classifyRightPress(0, dist + 1, time + 100, dist, time),
    'y distance and time both exceeded → drag',
  );
  assert.ok(
    classifyRightPress(4, 4, time + 50, dist, time),
    'diagonal 5.66px movement with sufficient hold → drag',
  );

  // Exactly at threshold distance (dragExceeded contract: exactly equal is NOT exceeded).
  assert.ok(
    !classifyRightPress(dist, 0, time + 100, dist, time),
    'exactly at distance threshold, sufficient time → click (strict >)',
  );
});

// Unhandled, did-fail-load leaves a live url view blank with no signal. The
// filter must ignore the two benign cases Chromium fires constantly, a
// cancelled or superseded navigation and any subframe failure, while surfacing
// a genuine main-frame failure.
test('shouldSurfaceFailLoad ignores aborted navigations and subframe failures', () => {
  // ERR_ABORTED (-3, Chromium's net error) on the main frame: the page or user
  // cancelled it, so it is not a failure.
  assert.ok(!shouldSurfaceFailLoad(-3, true));
  // A real error code but on a subframe (ad iframe, tracking pixel): benign.
  assert.ok(!shouldSurfaceFailLoad(-105, false));
  // ERR_ABORTED on a subframe: still benign; either condition alone disqualifies.
  assert.ok(!shouldSurfaceFailLoad(-3, false));
  // A genuine main-frame failure, such as ERR_CONNECTION_REFUSED (-102) or the
  // unreachable port the e2e drives, must surface.
  assert.ok(shouldSurfaceFailLoad(-102, true));
  assert.ok(shouldSurfaceFailLoad(-105, true));
});

test('failLoadMessage includes the failed URL and prefers the description over the raw code', () => {
  const withDesc = failLoadMessage('http://127.0.0.1:9/', 'ERR_CONNECTION_REFUSED', -102);
  assert.ok(withDesc.includes('http://127.0.0.1:9/'));
  assert.ok(withDesc.includes('ERR_CONNECTION_REFUSED'));
  // Falls back to the numeric code when Chromium gives no description.
  const noDesc = failLoadMessage('http://x/', '', -102);
  assert.ok(noDesc.includes('-102'));
  assert.ok(noDesc.includes('http://x/'));
});

test('renderProcessGoneMessage includes the url when known, omits it cleanly when not', () => {
  assert.equal(
    renderProcessGoneMessage('https://example.com/', 'crashed'),
    'page crashed (crashed): https://example.com/',
  );
  // getURL() after a crash can come back empty; the message must not end in a
  // dangling "page crashed: " with nothing after it.
  const noURL = renderProcessGoneMessage('', 'crashed');
  assert.equal(noURL, 'page crashed (crashed)');
  assert.ok(!noURL.endsWith(': '));
});

// The renderer's console, where the wasm client logs every surfaced notice, is
// invisible in the app's own stdout and stderr unless forwarded. The filter
// forwards only warnings (level 2) and errors (level 3), keeping info and
// verbose chatter out, with a [renderer:<level>] prefix so lines are
// attributable.
test('rendererLogLine forwards warnings and errors only, with a level prefix', () => {
  assert.equal(rendererLogLine(0, 'verbose chatter'), null);
  assert.equal(rendererLogLine(1, 'info chatter'), null);
  assert.equal(rendererLogLine(2, 'gridwell: [conflict:UpdateText] reloaded'), '[renderer:warning] gridwell: [conflict:UpdateText] reloaded');
  assert.equal(rendererLogLine(3, 'gridwell: [rpc:MoveTile] MoveTile failed: x'), '[renderer:error] gridwell: [rpc:MoveTile] MoveTile failed: x');
});

test('serializeHistory strips pageState, caps around the active index, rebases', () => {
  const mk = (n: number) => ({ url: `https://x/${n}`, title: `t${n}`, pageState: 'BIG' });
  // Single entry: nothing worth persisting, a plain loadURL restores it.
  assert.equal(serializeHistory([mk(1)], 0), '');
  // Two entries round-trip, pageState gone.
  const two = JSON.parse(serializeHistory([mk(1), mk(2)], 1));
  assert.deepEqual(two, { index: 1, entries: [{ url: 'https://x/1', title: 't1' }, { url: 'https://x/2', title: 't2' }] });
  // 60 entries, active at the end, cap 50: keep the last 50, index rebased.
  const many = Array.from({ length: 60 }, (_, i) => mk(i));
  const capped = JSON.parse(serializeHistory(many, 59, 50));
  assert.equal(capped.entries.length, 50);
  assert.equal(capped.index, 49);
  assert.equal(capped.entries[0].url, 'https://x/10');
});

test('parseHistory validates and clamps; garbage falls back to null', () => {
  const good = parseHistory('{"index":1,"entries":[{"url":"https://a","title":"A"},{"url":"https://b","title":"B"}]}');
  assert.ok(good);
  assert.equal(good!.index, 1);
  assert.equal(parseHistory(undefined), null);
  assert.equal(parseHistory(''), null);
  assert.equal(parseHistory('not json'), null);
  assert.equal(parseHistory('{"index":0,"entries":[]}'), null);
  assert.equal(parseHistory('{"index":0,"entries":[{"url":""}]}'), null);
  // An out-of-range index clamps rather than breaking restore.
  const clamped = parseHistory('{"index":99,"entries":[{"url":"https://a","title":""}]}');
  assert.equal(clamped!.index, 0);
});

test('classifyRightPress: a fast flick past the far threshold is a drag (#119)', () => {
  const dist = 4;
  const time = 200;
  // 30px in 50ms: an unambiguous drag even though the time gate fails.
  assert.equal(classifyRightPress(0, -30, 50, dist, time), true);
  // 10px in 50ms: past 4px but inside the far threshold and too fast, a click.
  assert.equal(classifyRightPress(10, 0, 50, dist, time), false);
  // Exactly at the far boundary stays a click; the comparison is strictly greater.
  assert.equal(classifyRightPress(24, 0, 50, dist, time), false);
});


// The live-view zoom forward accepts exactly the key set the wasm handler
// accepts. A drift here makes the two focus states zoom differently.
test('zoomChordKey matches the wasm chord set', () => {
  assert.equal(zoomChordKey({ key: '=', control: true }), '=');
  assert.equal(zoomChordKey({ key: '+', control: true }), '+');
  assert.equal(zoomChordKey({ key: '-', meta: true }), '-');
  assert.equal(zoomChordKey({ key: '0', control: true }), '0');
  assert.equal(zoomChordKey({ key: '=', control: false, meta: false }), '');
  assert.equal(zoomChordKey({ key: 'a', control: true }), '');
  assert.equal(zoomChordKey({ key: 'F11', control: true }), '');
});

// 'openExternal' is the one permission that hands a navigation to the OS, where
// xdg-open sends an unhandled protocol to the default browser. Deny it;
// everything else keeps Electron's default grant.
test('allowPermission denies exactly openExternal', () => {
  assert.equal(allowPermission('openExternal'), false);
  assert.equal(allowPermission('notifications'), true);
  assert.equal(allowPermission('clipboard-read'), true);
  assert.equal(allowPermission('media'), true);
});

// Only web urls open below. A non-web protocol opens nowhere: forwarding it
// would re-trigger the external-protocol path.
test('openBelowUrl forwards web urls and drops everything else', () => {
  assert.equal(openBelowUrl('https://example.com/x'), 'https://example.com/x');
  assert.equal(openBelowUrl('HTTP://example.com'), 'HTTP://example.com');
  assert.equal(openBelowUrl('zoommtg://zoom.us/join?confno=1'), null);
  assert.equal(openBelowUrl('mailto:a@b.c'), null);
  assert.equal(openBelowUrl('about:blank'), null);
  assert.equal(openBelowUrl(''), null);
});

// The revive tie-break: url_string is user-editable through the content door
// while url_history is written only by the freeze, so they can disagree.
// Restoring the stack would navigate to the page the user just typed over, so
// the address wins.
test('reviveNavigation: the edited address beats a stale back-stack', () => {
  const history = JSON.stringify({
    index: 1,
    entries: [
      { url: 'https://a.example/', title: 'a' },
      { url: 'https://b.example/', title: 'b' },
    ],
  });
  // Agreeing: the stack restores.
  assert.deepEqual(reviveNavigation('https://b.example/', history), {
    kind: 'restore',
    history: { index: 1, entries: [
      { url: 'https://a.example/', title: 'a' },
      { url: 'https://b.example/', title: 'b' },
    ] },
  });
  // The user edited the address: plain-load it, never the stale stack.
  assert.deepEqual(reviveNavigation('https://c.example/', history), { kind: 'load' });
  // Absent or invalid history: a plain load; a corrupt blob must never break
  // revive.
  assert.deepEqual(reviveNavigation('https://c.example/', ''), { kind: 'load' });
  assert.deepEqual(reviveNavigation('https://c.example/', '{broken'), { kind: 'load' });
});
