import { defineConfig } from '@playwright/test';

// End-to-end tests that drive the REAL Electron app (Go sidecar + wasm renderer)
// against a fresh on-disk DB. Each test launches its own Electron instance via
// _electron.launch (see e2e/fixtures.ts), so they run serially with a single
// worker — there is no shared state to parallelize and a process-per-test keeps
// the DB pristine. Invoked under xvfb by `make check-e2e`.
export default defineConfig({
  testDir: './e2e',
  // Sweep the leftovers of previous ABORTED runs (hard-killed workers skip
  // the per-test teardown) before this run starts — issue #108.
  globalSetup: './e2e/global-setup.ts',
  // Archive failure artifacts (traces, error contexts) after the run —
  // the NEXT run wipes test-results/ at start, so without this the usual
  // isolated rerun of a failing spec destroys the very evidence of the
  // failure being investigated.
  globalTeardown: './e2e/global-teardown.ts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  use: {
    trace: 'retain-on-failure',
  },
});
