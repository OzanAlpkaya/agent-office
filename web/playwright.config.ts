import { defineConfig } from "@playwright/test";

// Browser tests of the built UI against a mocked daemon (ADR 0025). Opt-in:
// make ui-test (needs `npx playwright install chromium-headless-shell` once).
const port = 4173;

export default defineConfig({
  testDir: "ui-tests",
  outputDir: "test-results/output",
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    browserName: "chromium",
    headless: true,
    viewport: { width: 1440, height: 900 },
  },
  webServer: {
    command: `npx vite preview --host 127.0.0.1 --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
