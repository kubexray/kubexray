import { defineConfig, devices } from "@playwright/test";

// Playwright runs this file in Node; the project's types are the browser's.
declare const process: { env: Record<string, string | undefined> };

// The UI end to end, on the public player with the recorded demo incident:
// deterministic, and no cluster needed. Run with `npm run e2e`.
export default defineConfig({
  testDir: "e2e",
  outputDir: "test-results",
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [["list"], ["html", { open: "never", outputFolder: "playwright-report" }]] : "list",
  use: {
    baseURL: "http://127.0.0.1:4173",
    viewport: { width: 1440, height: 1000 },
    colorScheme: "light",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "npm run build:pages && npx vite preview --mode pages --host 127.0.0.1 --port 4173 --strictPort",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 1000 } } }],
});
