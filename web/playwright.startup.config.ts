import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'

export default defineConfig({
  ...isolated,
  testMatch: 'startup-acceptance.spec.ts',
  timeout: 180_000,
  workers: 1,
  retries: 0,
  use: { ...isolated.use, baseURL: 'http://127.0.0.1:15175' },
  webServer: [{ command: 'npm run e2e:serve', url: 'http://127.0.0.1:15175',
    env: { E2E_API_PORT: '18083', E2E_WEB_PORT: '15175' }, reuseExistingServer: false, timeout: 180_000 }],
})
