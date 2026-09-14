import * as fs from 'node:fs';
import * as path from 'node:path';
import { removeTree } from './runtree';

// Drops the run's artifact snapshot (runtree.ts) and archives failure artifacts
// after the suite. Playwright wipes test-results/ at the start of the next run,
// which is usually the isolated rerun of the spec that just failed, so the
// full-suite trace would be gone before anyone read it. A hard-killed run skips
// this: its snapshot goes to the next run's sweep and its artifacts to that
// wipe.
const KEEP = 5;

export default function globalTeardown(): void {
  removeTree();
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
