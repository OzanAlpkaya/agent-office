import { defineConfig } from "vitest/config";

// Unit tests for pure UI logic (ADR 0025). Playwright specs live in ui-tests/
// and run separately (make ui-test).
export default defineConfig({
  test: { include: ["src/**/*.test.ts"], environment: "node" },
});
