import { defineConfig } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

// Dedicated fixtures; never connect to the developer's 8080/5173 instance.
const apiPort = Number(process.env.E2E_API_PORT || 18080)
const webPort = Number(process.env.E2E_WEB_PORT || 15173)
if (![apiPort, webPort].every(p => Number.isInteger(p) && p > 1024 && p < 65536 && p !== 8080 && p !== 5173) || apiPort === webPort) throw new Error('Unsafe E2E ports')
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
    baseURL: `http://127.0.0.1:${webPort}`,
    headless: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  outputDir: resolve(artifactDir, 'browser'),
  reporter: [['list'], ['json', { outputFile: resolve(artifactDir, 'results.json') }]],
  webServer: [
    {
      command: `${process.env.E2E_SERVER_BINARY ? `"${resolve(process.env.E2E_SERVER_BINARY)}"` : 'go run ./cmd/server'} -testmode -port ${apiPort} -db "${resolve(artifactDir, 'test.db')}"`,
      cwd: '../server',
      url: `http://127.0.0.1:${apiPort}/api/v1/bootstrap/status`,
      reuseExistingServer: false,
      timeout: 120_000,
    },
    {
      command: 'npm run e2e:serve',
      url: `http://127.0.0.1:${webPort}`,
      reuseExistingServer: false,
      timeout: 180_000,
    },
  ],
})
