// Whether a pane's mirror capture is failing and whether anyone has been told.
// A frozen preview must leave evidence, and the pump captures on a timer, so
// the report fires on the transition, not per frame. webviews.ts runs the
// capture and keeps the state on the entry.

import type { NoticeSeverity } from './ipc';

// 'empty' is a frame that encoded to nothing, 'view-gone' a destroyed view.
// Everything but 'ok' counts toward the streak, because a frozen preview looks
// the same to the user however the capture died.
export type AttemptKind = 'ok' | 'empty' | 'timeout' | 'rejected' | 'view-gone';

export interface StreakState {
  // Separates a mirror that froze from one that has not started: Chromium
  // answers capturePage with an empty image for the first frames after a view
  // is placed, and those empties say nothing.
  everCaptured: boolean;
  // Consecutive failures; failing means failures > 0.
  failures: number;
}

// Failing carries the reason that opened the streak; recovered carries how many
// captures were lost.
export type StreakReport =
  | { kind: 'failing'; reason: AttemptKind }
  | { kind: 'recovered'; afterFailures: number };

export interface StreakDecision {
  state: StreakState;
  report: StreakReport | null;
}

// FRESH is frozen because every entry starts from this one value and
// decideStreak only returns new ones.
export const FRESH: StreakState = Object.freeze({ everCaptured: false, failures: 0 });

export function decideStreak(prev: StreakState, kind: AttemptKind): StreakDecision {
  if (kind === 'ok') {
    // A good frame ends the streak whatever opened it, since a reloaded
    // renderer captures again. Recovery is reported only where a failure was,
    // so the two always pair.
    const reportable = prev.everCaptured && prev.failures > 0;
    return {
      state: { everCaptured: true, failures: 0 },
      report: reportable ? { kind: 'recovered', afterFailures: prev.failures } : null,
    };
  }
  return {
    state: { everCaptured: prev.everCaptured, failures: prev.failures + 1 },
    report: prev.everCaptured && prev.failures === 0 ? { kind: 'failing', reason: kind } : null,
  };
}

// What a report says and how loudly, so the wording and the severity are one
// decision with a test rather than a string built at the call site. detail is
// capture.describeAttempt for the attempt that opened the streak; only the
// failing arm shows it.
export function streakNotice(paneId: string, report: StreakReport, detail: string): { severity: NoticeSeverity; message: string } {
  if (report.kind === 'failing') {
    return { severity: 'error', message: `pane ${paneId}: mirror capture failing: ${detail}` };
  }
  const n = report.afterFailures;
  // A mirror that is live again is information. Reported as an error it would
  // paint the strip's red row a second time, at the moment it stopped being
  // true.
  return {
    severity: 'info',
    message: `pane ${paneId}: mirror capture recovered after ${n} failed ${n === 1 ? 'capture' : 'captures'}`,
  };
}
