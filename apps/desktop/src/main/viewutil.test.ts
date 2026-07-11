import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  SESSION_PARTITION,
  roundBounds,
  boundsEqual,
  controlVisible,
  controlBounds,
  parkedBounds,
  namePillBounds,
  cookieDomainMatches,
  storageOriginsFor,
  minWidthZoomFactor,
  composeZoom,
  serializeHistory,
  parseHistory,
  URL_MIN_LAYOUT_WIDTH,
  PARK_COORD,
  dragExceeded,
  classifyRightPress,
  sanitizeUserAgent,
  RIGHT_DRAG_THRESHOLD,
  RIGHT_DRAG_TIME_MS,
  shouldSurfaceFailLoad,
  failLoadMessage,
  renderProcessGoneMessage,
  ERR_ABORTED,
  rendererLogLine,
  partitionFor,
  proxyRulesFor,
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

test('composeZoom multiplies layout and user zoom, clamped to the Chromium floor', () => {
  assert.equal(composeZoom(1, 1.5), 1.5); // wide pane, user zoomed in
  assert.equal(composeZoom(0.5, 2), 1); // they multiply
  assert.equal(composeZoom(1, 0), 1); // 0 = unset → 1.0
  assert.equal(composeZoom(0.25, 0.5), 0.25); // floor
});

test('URL_MIN_LAYOUT_WIDTH pins the production zoom-to-fit threshold', () => {
  // Owner-tuned (issue #87): below 800 the page keeps an 800px desktop layout
  // scaled to fit, instead of reflowing to a cramped semi-mobile layout.
  assert.equal(URL_MIN_LAYOUT_WIDTH, 800);
  assert.equal(minWidthZoomFactor(900, URL_MIN_LAYOUT_WIDTH), 1);
  assert.equal(minWidthZoomFactor(400, URL_MIN_LAYOUT_WIDTH), 0.5);
});

// Regression guards for issue #33 mechanism B and issue #119: a fast trackpad
// tap that drifts a few pixels past the 4px threshold is still a click — the
// context menu must fire — while a fast FLICK past the far threshold is a
// gesture regardless of duration, and an intentional hold-and-drag exceeding
// both small thresholds is a gesture too.
test('classifyRightPress requires both distance and time to classify as drag', () => {
  const dist = RIGHT_DRAG_THRESHOLD; // 4 px
  const time = RIGHT_DRAG_TIME_MS; // 200 ms

  // Neither condition met → click.
  assert.ok(!classifyRightPress(0, 0, 0, dist, time), 'zero movement, zero time → click');

  // Distance exceeded but button released fast (trackpad jitter) → click.
  assert.ok(
    !classifyRightPress(dist + 1, 0, time - 1, dist, time),
    'distance exceeded but duration < threshold → click (jitter case)',
  );
  assert.ok(
    !classifyRightPress(0, dist + 1, time - 1, dist, time),
    'y-only distance exceeded, short hold → click',
  );

  // Time exceeded but barely any movement → click (user just held the button).
  assert.ok(
    !classifyRightPress(0, 0, time + 100, dist, time),
    'long hold but no movement → click',
  );

  // Both conditions met → drag (intentional pane gesture).
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

// Regression guard for issue #46 point 3: did-fail-load was completely
// unhandled, so a live URL view could go blank with zero signal. The filter
// must ignore the two benign cases Chromium fires constantly (a cancelled/
// superseded navigation, and any subframe failure) while surfacing a genuine
// main-frame failure.
test('shouldSurfaceFailLoad ignores aborted navigations and subframe failures', () => {
  // ERR_ABORTED on the main frame: the page/user cancelled it — not a failure.
  assert.ok(!shouldSurfaceFailLoad(ERR_ABORTED, true));
  // A real error code but on a subframe (ad iframe, tracking pixel): benign.
  assert.ok(!shouldSurfaceFailLoad(-105, false));
  // ERR_ABORTED on a subframe: still benign (both conditions independently disqualify).
  assert.ok(!shouldSurfaceFailLoad(ERR_ABORTED, false));
  // A genuine main-frame failure (e.g. ERR_CONNECTION_REFUSED = -102, or the
  // unreachable-port case the e2e drives) must surface.
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
  // Best-effort getURL() after a crash can come back empty — no dangling
  // "page crashed: " with nothing after it.
  const noURL = renderProcessGoneMessage('', 'crashed');
  assert.equal(noURL, 'page crashed (crashed)');
  assert.ok(!noURL.endsWith(': '));
});

// "All errors should be printed to the logs": the renderer's console (where
// the wasm client logs every surfaced notice) is invisible in the app's own
// stdout/stderr unless forwarded. The filter forwards only warnings (level 2)
// and errors (level 3) — info/verbose chatter stays out of the log — with a
// [renderer:<level>] prefix so log lines are attributable.
test('rendererLogLine forwards warnings and errors only, with a level prefix', () => {
  assert.equal(rendererLogLine(0, 'verbose chatter'), null);
  assert.equal(rendererLogLine(1, 'info chatter'), null);
  assert.equal(rendererLogLine(2, 'gridwell: [conflict:UpdateText] reloaded'), '[renderer:warning] gridwell: [conflict:UpdateText] reloaded');
  assert.equal(rendererLogLine(3, 'gridwell: [rpc:MoveTile] MoveTile failed: x'), '[renderer:error] gridwell: [rpc:MoveTile] MoveTile failed: x');
});

test('partitionFor flattens a namespace chain to a distinct, stable partition', () => {
  assert.equal(partitionFor('abc123'), 'persist:plugin-abc123');
  // A remote plugin through a mount keys per REMOTE plugin, not per mount.
  assert.equal(partitionFor('ssh1/rp1'), 'persist:plugin-ssh1-rp1');
  assert.notEqual(partitionFor('ssh1/rp1'), partitionFor('ssh1/rp2'));
  assert.equal(partitionFor(''), 'persist:gridwell');
});

test('proxyRulesFor accepts a socks5 endpoint and rejects garbage', () => {
  assert.equal(proxyRulesFor('socks5://127.0.0.1:41234'), 'socks5://127.0.0.1:41234');
  assert.equal(proxyRulesFor(''), '');
  assert.equal(proxyRulesFor('http://evil/ '), '');
  assert.equal(proxyRulesFor('socks5://a b'), '');
});

test('serializeHistory strips pageState, caps around the active index, rebases', () => {
  const mk = (n: number) => ({ url: `https://x/${n}`, title: `t${n}`, pageState: 'BIG' });
  // Single entry: nothing worth persisting (plain loadURL restores it).
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
  const dist = RIGHT_DRAG_THRESHOLD;
  const time = RIGHT_DRAG_TIME_MS;
  // 30px up in 50ms: unambiguous drag even though the time gate fails.
  assert.equal(classifyRightPress(0, -30, 50, dist, time), true);
  // 10px in 50ms: past 4px but inside the far threshold and too fast — click.
  assert.equal(classifyRightPress(10, 0, 50, dist, time), false);
  // Exactly at the far boundary stays a click (strictly-greater semantics).
  assert.equal(classifyRightPress(24, 0, 50, dist, time), false);
});

test('namePillBounds centers the bubble at the top, shrinking into narrow views', () => {
  const b = { x: 100, y: 50, width: 400, height: 300 };
  assert.deepEqual(namePillBounds(b, 240, 24, 4), { x: 180, y: 54, width: 240, height: 24 });
  // Narrower than the pill: it shrinks to fit inside the margins.
  const narrow = namePillBounds({ x: 0, y: 0, width: 100, height: 300 }, 240, 24, 4);
  assert.equal(narrow.width, 92);
  assert.equal(narrow.x, 4);
});

// Issue #136 / #177: the site-clearing cookie match — one SITE (registrable
// domain), which includes SIBLING subdomains: Google's login state lives on
// accounts.google.com while the user clears from mail.google.com, and a
// clear that misses it strands a broken login forever ("cleared, logged in,
// same error"). Over-broad matching would log the user out of unrelated
// sites; the public-suffix list keeps foo.co.uk from matching bar.co.uk.
test('cookieDomainMatches spans one site incl. siblings, never unrelated sites', () => {
  assert.equal(cookieDomainMatches('accounts.google.com', '.google.com'), true);
  assert.equal(cookieDomainMatches('google.com', 'accounts.google.com'), true);
  assert.equal(cookieDomainMatches('google.com', 'google.com'), true);
  assert.equal(cookieDomainMatches('google.com', '.GOOGLE.com'), true);
  // Sibling subdomains of one site (the Gmail clear-and-relogin loop).
  assert.equal(cookieDomainMatches('mail.google.com', 'accounts.google.com'), true);
  assert.equal(cookieDomainMatches('mail.google.com', '.accounts.google.com'), true);
  assert.equal(cookieDomainMatches('calendar.google.com', 'mail.google.com'), true);
  // Never across sites — including same-public-suffix neighbours.
  assert.equal(cookieDomainMatches('notgoogle.com', '.google.com'), false);
  assert.equal(cookieDomainMatches('google.com', 'oogle.com'), false);
  assert.equal(cookieDomainMatches('google.com', 'github.com'), false);
  assert.equal(cookieDomainMatches('foo.co.uk', 'bar.co.uk'), false);
  assert.equal(cookieDomainMatches('a.foo.co.uk', 'b.foo.co.uk'), true);
  // Hosts outside the public-suffix world (IPs, single labels) keep the
  // exact/ancestor rules and never sibling-match.
  assert.equal(cookieDomainMatches('127.0.0.1', '127.0.0.1'), true);
  assert.equal(cookieDomainMatches('localhost', 'localhost'), true);
  assert.equal(cookieDomainMatches('mylocalhost', 'otherlocalhost'), false);
  assert.equal(cookieDomainMatches('', 'google.com'), false);
});

// Issue #177: the storage-clear scope is derived from the SAME matched cookie
// set (one owner) — every matched cookie host contributes its https/http
// origins, plus the page origin itself (which alone carries the port).
test('storageOriginsFor derives origins from matched cookie hosts', () => {
  const got = storageOriginsFor('http://mail.google.com:8080', [
    '.google.com',
    'accounts.google.com',
    'accounts.google.com', // dedup
  ]);
  assert.deepEqual(got.sort(), [
    'http://accounts.google.com',
    'http://google.com',
    'http://mail.google.com:8080',
    'https://accounts.google.com',
    'https://google.com',
  ]);
});
