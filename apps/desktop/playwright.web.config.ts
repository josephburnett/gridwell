import { defineConfig } from '@playwright/test';

// Browser-mode e2e (`make check-web`): the same wasm client `make check-e2e`
// exercises under Electron, but served by `gridwell serve` and loaded in a plain
// Chromium page, with no Electron shell and no window.gridwell bridge. This is
// the only gate that sees the degraded phone and tablet client: caps gating,
// no-live affordances, and the touch gesture layer.
//
// It uses the system chromium through executablePath rather than a Playwright
// browser download, keeping the build fully offline. Override with
// GRIDWELL_WEB_CHROMIUM if the binary lives elsewhere.
export default defineConfig({
  testDir: './e2e-web',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  use: {
    trace: 'retain-on-failure',
    hasTouch: true,
    viewport: { width: 1024, height: 768 },
    launchOptions: {
      executablePath: process.env.GRIDWELL_WEB_CHROMIUM || '/usr/bin/chromium',
    },
  },
});
