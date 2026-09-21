import { defineConfig } from '@playwright/test';

// End-to-end tests that drive the real Electron app, Go sidecar and wasm
// renderer against a fresh on-disk database. Each test launches its own Electron
// instance through _electron.launch (see e2e/fixtures.ts), so they run serially
// on a single worker. `make check-e2e` runs this under xvfb.
export default defineConfig({
  testDir: './e2e',
  // Sweep the leftovers of previous aborted runs before this one starts; a
  // hard-killed worker skips the per-test teardown.
  globalSetup: './e2e/global-setup.ts',
  // Archive failure artifacts after the run. The next run wipes test-results/ at
  // start, so without this the isolated rerun of a failing spec destroys the
  // evidence.
  globalTeardown: './e2e/global-teardown.ts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  // The JSON report is how a retry survives the run: with --retries the list
  // reporter prints "1 flaky" and Playwright still exits 0, so the gate reads
  // this file afterwards (scripts/flaky-report.mjs, invoked by check-e2e).
  reporter: [['list'], ['json', { outputFile: 'playwright-report/e2e.json' }]],
  use: {
    trace: 'retain-on-failure',
  },
});
