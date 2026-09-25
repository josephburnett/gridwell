import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decideStreak, streakNotice, FRESH, AttemptKind, StreakDecision, StreakState } from './capturestreak';

// Every failure kind, so no arm can be added without a row here.
const FAILURES: AttemptKind[] = ['empty', 'timeout', 'rejected', 'view-gone'];

// A mirror that has captured before, the state every frozen-preview row starts
// in.
function live(failures = 0): StreakState {
  return { everCaptured: true, failures };
}

const TABLE: { name: string; prev: StreakState; kind: AttemptKind; want: StreakDecision }[] = [
  {
    name: '1. the first frame a pane ever produces says nothing',
    prev: FRESH,
    kind: 'ok',
    want: { state: live(0), report: null },
  },
  {
    name: '2. a healthy mirror that captures again says nothing',
    prev: live(0),
    kind: 'ok',
    want: { state: live(0), report: null },
  },
  // 3-6: one lost frame is a quarter second of a stale mirror, which nobody
  // can see. Only a mirror that stays frozen is worth a row on the strip.
  ...FAILURES.map((kind, i) => ({
    name: `${i + 3}. a single ${kind} is not yet a frozen mirror`,
    prev: live(0),
    kind,
    want: { state: live(1), report: null },
  })),
  // 7-10: it still is not capturing a tick later, so now it is frozen.
  ...FAILURES.map((kind, i) => ({
    name: `${i + 7}. a second ${kind} in a row opens the streak and is reported`,
    prev: live(1),
    kind,
    want: { state: live(2), report: { kind: 'failing' as const, reason: kind } },
  })),
  {
    name: '11. a third failure counts but does not report again',
    // The pump captures every live pane on a timer; reporting per frame would
    // bury the log.
    prev: live(2),
    kind: 'timeout',
    want: { state: live(3), report: null },
  },
  {
    name: '12. a failure of a different kind mid-streak is still silent',
    prev: live(3),
    kind: 'rejected',
    want: { state: live(4), report: null },
  },
  {
    name: '13. a success after a reported streak reports recovery',
    prev: live(3),
    kind: 'ok',
    want: { state: live(0), report: { kind: 'recovered', afterFailures: 3 } },
  },
  {
    name: '14. a lone failure that heals on the next tick says nothing either',
    // Nothing was reported failing, so a recovery row would be the only
    // notice of a miss too short to see.
    prev: live(1),
    kind: 'ok',
    want: { state: live(0), report: null },
  },
  {
    name: '15. the next success after a recovery is silent',
    // Recovery is reported once. The count is back to 0, so the arm above
    // cannot fire again until a new streak reaches the threshold.
    prev: live(0),
    kind: 'ok',
    want: { state: live(0), report: null },
  },
  {
    name: '16. a destroyed view recovers like anything else',
    // A reloaded renderer captures again, so view-gone closes like any other
    // failure kind.
    prev: live(2),
    kind: 'ok',
    want: { state: live(0), report: { kind: 'recovered', afterFailures: 2 } },
  },
  {
    name: '17. a new failure after a recovery opens a new streak',
    prev: live(0),
    kind: 'empty',
    want: { state: live(1), report: null },
  },
  {
    name: '18. a view that has never painted is not a frozen mirror',
    // Chromium answers capturePage with an empty image for the first frames
    // after a place, while the pane still shows its stored preview, so there is
    // nothing frozen to report.
    prev: FRESH,
    kind: 'empty',
    want: { state: { everCaptured: false, failures: 1 }, report: null },
  },
  {
    name: '19. warm-up failures keep counting but stay silent',
    prev: { everCaptured: false, failures: 1 },
    kind: 'timeout',
    want: { state: { everCaptured: false, failures: 2 }, report: null },
  },
  {
    name: '20. the first real frame after warm-up reports no recovery',
    // Nothing was ever reported failing, so there is nothing to recover from.
    prev: { everCaptured: false, failures: 2 },
    kind: 'ok',
    want: { state: live(0), report: null },
  },
];

for (const c of TABLE) {
  test(`decideStreak: ${c.name}`, () => {
    assert.deepEqual(decideStreak(c.prev, c.kind), c.want);
  });
}

test('decideStreak: a placed pane warms up quietly, then reports one freeze and one recovery', () => {
  const reports: string[] = [];
  let state = FRESH;
  const feed = (kind: AttemptKind) => {
    const d = decideStreak(state, kind);
    state = d.state;
    if (d.report) reports.push(d.report.kind);
  };
  feed('empty'); // not painted yet
  feed('empty');
  feed('ok'); // the mirror is live
  feed('timeout'); // one lost frame, too short to see
  feed('timeout'); // and now it is frozen
  feed('empty');
  feed('rejected');
  feed('ok'); // reloaded
  feed('ok');
  assert.deepEqual(reports, ['failing', 'recovered']);
  assert.deepEqual(state, { everCaptured: true, failures: 0 });
});

// What each report says and how loudly. The strip groups by source, so a
// recovery reported as an error paints the same red row the failure did, at
// the moment the mirror came back.

test('streakNotice: a failing mirror is an error, and names the pane and the reason', () => {
  const n = streakNotice('w3:p322', { kind: 'failing', reason: 'rejected' }, 'capturePage failed: Error: UnknownVizError');
  assert.equal(n.severity, 'error');
  assert.equal(n.message, 'pane w3:p322: mirror capture failing: capturePage failed: Error: UnknownVizError');
});

test('streakNotice: a recovery is information, not an error', () => {
  const n = streakNotice('w3:p322', { kind: 'recovered', afterFailures: 2 }, 'ok');
  assert.equal(n.severity, 'info');
  assert.equal(n.message, 'pane w3:p322: mirror capture recovered after 2 failed captures');
});

test('streakNotice: one lost capture reads in the singular', () => {
  const n = streakNotice('p1', { kind: 'recovered', afterFailures: 1 }, 'ok');
  assert.equal(n.message, 'pane p1: mirror capture recovered after 1 failed capture');
});
