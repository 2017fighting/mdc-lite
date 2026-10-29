import { defineConfig } from 'vitest/config'

// Frontend tests run in jsdom against injected callbacks, not a live server: the
// API contract is already covered by the Go contract tests, so what is tested here
// is the browser side's own logic — query building, the scope ledger's rules, the
// needs-number form, and the four-state components.
export default defineConfig({
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    restoreMocks: true,
  },
})
