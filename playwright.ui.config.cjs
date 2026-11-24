const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './e2e',
  testMatch: '**/fleethouse-ui.spec.cjs',
  timeout: 30_000,
  expect: { timeout: 8_000 },
  use: {
    baseURL: 'http://127.0.0.1:4174',
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'node e2e/ui-mock-server.cjs',
    url: 'http://127.0.0.1:4174/healthz',
    reuseExistingServer: false,
  },
});
