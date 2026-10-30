import { defineConfig, devices } from '@playwright/test'

// The interface arm of the acceptance environment: a real browser against the product
// server the environment started, not a jsdom render of a component.
//
// It is not part of `make check`. The pull-request gate stays fast (vitest, jsdom);
// this config is what `acceptance/playwright.go` runs once per acceptance run, after
// `npm ci` and `npm run build` have put the interface into the binary's embed.
//
//   MDC_E2E_BASE_URL       the product's address (set by the acceptance run)
//   MDC_E2E_*              the ids and names the arm found over the API
export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  // One worker, in file order: the needs-number test changes the run's state, and the
  // view tests are not independent of it.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['line']],
  use: {
    baseURL: process.env.MDC_E2E_BASE_URL,
    // No traces and no screenshots by default: the failure output is the page's own
    // assertions, and the acceptance run keeps the server log beside it.
    trace: 'off',
    screenshot: 'off',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
