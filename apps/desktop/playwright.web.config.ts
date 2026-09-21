import { defineConfig } from '@playwright/test';

// Browser-mode e2e (`make check-web`). The same wasm client `make check-e2e`
// exercises under Electron, served by `gridwell serve` and loaded in a plain
// Chromium page with no Electron shell and no window.gridwell bridge. It is the
// only gate that sees the degraded phone and tablet client: caps gating, no-live
// affordances, and the touch gesture layer.
//
// It uses the system chromium through executablePath rather than a Playwright
// browser download, so the build stays offline. Override with
// GRIDWELL_WEB_CHROMIUM if the binary lives elsewhere.
export default defineConfig({
  testDir: './e2e-web',
  // The same two hooks the Electron gate runs: the leak sweep, and the snapshot
  // of the artifacts this run tests (e2e/runtree.ts), which the served node and
  // its plugins are launched from.
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  // Its own file, so a web run never reads the Electron run's retries. See
  // playwright.config.ts.
  reporter: [['list'], ['json', { outputFile: 'playwright-report/web.json' }]],
  use: {
    trace: 'retain-on-failure',
    hasTouch: true,
    viewport: { width: 1024, height: 768 },
    launchOptions: {
      executablePath: process.env.GRIDWELL_WEB_CHROMIUM || '/usr/bin/chromium',
    },
  },
});
