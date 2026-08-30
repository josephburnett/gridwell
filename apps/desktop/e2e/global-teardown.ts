import * as fs from 'node:fs';
import * as path from 'node:path';

// Runs once after the suite: preserve failure artifacts before something
// destroys them. Playwright wipes test-results/ at the start of the next run,
// and the next run is usually the quick isolated rerun of the spec that just
// failed, so the full-suite trace would be gone before anyone read past the log
// tail. trace: retain-on-failure means test-results holds real entries exactly
// when something failed, so they are archived next door, keeping the last few
// runs' worth.
//
// A hard-killed run skips globalTeardown, and its artifacts sit in test-results
// until the next run's start-wipe. That gap is accepted: the common
// evidence-destroyer is the vindication rerun after a red suite, and the red run
// ended normally and archived its own artifacts before any rerun could start.
const KEEP = 5;

export default function globalTeardown(): void {
  const desktop = path.resolve(__dirname, '..');
  const results = path.join(desktop, 'test-results');
  let entries: string[] = [];
  try {
    entries = fs.readdirSync(results).filter((e) => e !== '.last-run.json');
  } catch {
    return; // no results dir: nothing failed, nothing to keep
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
