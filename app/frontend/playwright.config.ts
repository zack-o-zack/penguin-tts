import { defineConfig } from "@playwright/test";

const isCI = Boolean(process.env.CI);
const baseURL = "http://127.0.0.1:9473";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: isCI,
  failOnFlakyTests: isCI,
  retries: isCI ? 1 : 0,
  workers: isCI ? 1 : 2,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  outputDir: "./test-results",
  reporter: [
    [isCI ? "github" : "list"],
    ["html", { outputFolder: "playwright-report", open: "never" }],
  ],
  use: {
    baseURL,
    viewport: { width: 1100, height: 760 },
    locale: "en-US",
    timezoneId: "UTC",
    colorScheme: "dark",
    reducedMotion: "reduce",
    serviceWorkers: "block",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    { name: "chromium", use: { browserName: "chromium" } },
    { name: "webkit", use: { browserName: "webkit" } },
  ],
  webServer: {
    command: "pnpm --filter @penguin/app serve:e2e",
    url: baseURL,
    timeout: 180_000,
    reuseExistingServer: false,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      CGO_ENABLED: "0",
      WAILS_SERVER_HOST: "127.0.0.1",
      WAILS_SERVER_PORT: "9473",
    },
  },
});
