import { defineConfig } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

// Dedicated fixtures; never connect to the developer's 8080/5173 instance.
const artifactDir = resolve('.test-artifacts', `closure-${Date.now()}-${process.pid}`)
mkdirSync(artifactDir, { recursive: true })
export default defineConfig({
  testDir: 'e2e',
  testMatch: 'closure-regression.spec.js',
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL: 'http://127.0.0.1:15173',
    headless: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  outputDir: resolve(artifactDir, 'browser'),
  reporter: [['list'], ['json', { outputFile: resolve(artifactDir, 'results.json') }]],
  webServer: [
    {
      command: `go run ./cmd/server -testmode -port 18080 -db "${resolve(artifactDir, 'test.db')}"`,
      cwd: '../server',
      url: 'http://127.0.0.1:18080/api/v1/bootstrap/status',
      reuseExistingServer: false,
      timeout: 120_000,
    },
    {
      command: 'npm run e2e:serve',
      url: 'http://127.0.0.1:15173',
      reuseExistingServer: false,
      timeout: 180_000,
    },
  ],
})
