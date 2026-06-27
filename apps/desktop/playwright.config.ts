import { defineConfig } from '@playwright/test';

// End-to-end tests that drive the REAL Electron app (Go sidecar + wasm renderer)
// against a fresh on-disk DB. Each test launches its own Electron instance via
// _electron.launch (see e2e/fixtures.ts), so they run serially with a single
// worker — there is no shared state to parallelize and a process-per-test keeps
// the DB pristine. Invoked under xvfb by `make check-e2e`.
export default defineConfig({
  testDir: './e2e',
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
