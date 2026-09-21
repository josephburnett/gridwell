#!/usr/bin/env node
// flaky-report: fails a gate on a retry that passed. Playwright exits 0 when a
// spec fails and passes on the retry, so without this the run's only record of
// the failure is a line in a log nobody reads. A flake is a bug with a
// diagnosis pending (docs/flake-ledger.md), so a flaky spec with no ledger row
// fails the gate.
//
//   node scripts/flaky-report.mjs <playwright json report> [ledger.md]
//
// Exit 0: no retry passed, or every spec that needed one is ledgered.
// Exit 1: a retry passed a spec the ledger does not carry.
// Exit 2: the report is missing or unreadable.
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const VERDICT = 'a retry passed this spec; ledger it with evidence or fix it';

// flakySpecs are the specs whose result needed a retry. A report's spec.file
// is relative to config.rootDir, which is absolute in a real run and relative
// to the repo root in a hand-written fixture.
function flakySpecs(report, repoRoot) {
  const rootDir = path.resolve(repoRoot, report?.config?.rootDir ?? '.');
  const out = [];
  const walk = (suite) => {
    for (const spec of suite.specs ?? []) {
      if (!(spec.tests ?? []).some((t) => t.status === 'flaky')) continue;
      out.push({
        spec: path.relative(repoRoot, path.resolve(rootDir, spec.file)),
        line: spec.line,
        title: spec.title,
      });
    }
    for (const child of suite.suites ?? []) walk(child);
  };
  for (const suite of report?.suites ?? []) walk(suite);
  return out;
}

// ledgered reads the spec paths docs/flake-ledger.md carries: one per table
// row, backticked, in the row's first cell. test/boundary/flakeledger_test.go
// pins that same convention from the other side.
function ledgered(markdown) {
  const specs = new Set();
  for (const line of markdown.split('\n')) {
    const row = line.trim();
    if (!row.startsWith('|')) continue;
    const cell = row.slice(1).split('|')[0];
    const quoted = cell.match(/`([^`]+)`/);
    if (quoted) specs.add(quoted[1]);
  }
  return specs;
}

function main(argv) {
  const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
  const [reportPath, ledgerPath = path.join(repoRoot, 'docs', 'flake-ledger.md')] = argv;
  if (!reportPath) {
    console.error('usage: node scripts/flaky-report.mjs <playwright json report> [ledger.md]');
    return 2;
  }
  let report;
  try {
    report = JSON.parse(fs.readFileSync(reportPath, 'utf8'));
  } catch (err) {
    console.error(`flaky-report: cannot read ${reportPath}: ${err.message}`);
    return 2;
  }
  const flaky = flakySpecs(report, repoRoot);
  if (flaky.length === 0) {
    console.log('flaky-report: no retry passed a spec');
    return 0;
  }
  const known = ledgered(fs.readFileSync(ledgerPath, 'utf8'));
  let unledgered = 0;
  for (const f of flaky) {
    const where = `${f.spec}:${f.line} › ${f.title}`;
    if (known.has(f.spec)) {
      console.log(`flaky-report: ${where} — a retry passed it; ledgered`);
      continue;
    }
    console.error(`flaky-report: ${where} — ${VERDICT}`);
    unledgered++;
  }
  return unledgered > 0 ? 1 : 0;
}

process.exit(main(process.argv.slice(2)));
