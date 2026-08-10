import { defineConfig, devices } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

const e2ePort = process.env.GUGU_E2E_PORT ?? "18080";
const e2eOrigin = `http://127.0.0.1:${e2ePort}`;
const webDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(webDirectory, "..");

export default defineConfig({
  testDir: "./e2e",
  outputDir: "./test-results/run",
  fullyParallel: false,
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  failOnFlakyTests: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  timeout: 30_000,
  expect: { timeout: 8_000 },
  reporter: [
    ["line"],
    ["html", { outputFolder: "playwright-report", open: "never" }],
  ],
  use: {
    baseURL: e2eOrigin,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  webServer: {
    command: "go run ./cmd/control-plane",
    cwd: repositoryRoot,
    env: {
      ...process.env,
      GUGU_ENVIRONMENT: "development",
      GUGU_HTTP_ADDR: `127.0.0.1:${e2ePort}`,
      GUGU_WEB_ROOT: path.resolve(webDirectory, "dist"),
      GUGU_DEV_ADMIN_EMAIL: "e2e-admin@gugu.local",
      GUGU_DEV_ADMIN_PASSWORD: "browser-e2e-only-2026",
      GUGU_DEV_AGENT_TOKEN: "browser-e2e-agent-token-2026",
      GUGU_DEV_DATA_ROOT: path.resolve(webDirectory, "test-results", "run", "server-data"),
      GUGU_DEV_OPERATION_LATENCY: "0s",
    },
    url: `${e2eOrigin}/readyz`,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
