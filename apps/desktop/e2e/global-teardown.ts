import * as fs from 'node:fs';
import * as path from 'node:path';

// Runs once after the suite: preserve failure artifacts before something
// destroys them. Playwright WIPES test-results/ at the START of the next
// run — and "the next run" is usually the quick isolated rerun of the spec
// that just failed, so the full-suite trace was routinely gone by the time
// anyone read past the log tail (the 2026-08-06 failures were diagnosable
// only because a copy of the list output happened to survive in a
// scratchpad). trace: retain-on-failure means test-results holds real
// entries exactly when something failed; archive them next door and keep
// the last few runs' worth.
//
// A hard-killed run (SIGKILL, power) skips globalTeardown; its artifacts
// sit in test-results until the next run's start-wipe. That gap is
// accepted: the common evidence-destroyer is the vindication rerun after
// a red suite, and that path is covered because the red run ended normally
// and archived its own artifacts here before any rerun could start.
const KEEP = 5;

export default function globalTeardown(): void {
  const desktop = path.resolve(__dirname, '..');
  const results = path.join(desktop, 'test-results');
  let entries: string[] = [];
  try {
    entries = fs.readdirSync(results).filter((e) => e !== '.last-run.json');
  } catch {
    return; // no results dir — nothing failed, nothing to keep
  }
  if (entries.length === 0) return;
  const arch = path.join(desktop, 'e2e-artifacts');
  fs.mkdirSync(arch, { recursive: true });
  const stamp = new Date().toISOString().replace(/[:.]/g, '-');
  const dst = path.join(arch, stamp);
  fs.cpSync(results, dst, { recursive: true });
  console.log(`[e2e] failure artifacts archived to ${dst}`);
  const runs = fs.readdirSync(arch).sort();
  for (const old of runs.slice(0, Math.max(0, runs.length - KEEP))) {
    fs.rmSync(path.join(arch, old), { recursive: true, force: true });
  }
}
