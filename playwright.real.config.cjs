const { defineConfig } = require("@playwright/test");

const port = Number(process.env.E2E_REAL_PORT || 4174);
module.exports = defineConfig({
  testDir: "./e2e",
  testMatch: "external-import.real.spec.cjs",
  timeout: 30_000,
  expect: { timeout: 8_000 },
  workers: 1,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    locale: "en-US",
    timezoneId: "UTC",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "bun e2e/real-server.cjs",
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: false,
    timeout: 120_000,
    gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 },
  },
});
