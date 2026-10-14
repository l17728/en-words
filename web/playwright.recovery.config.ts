import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'
export default defineConfig({
  ...isolated,
  testMatch: 'recovery-regression.spec.ts',
  timeout: 180_000,
  use: { ...isolated.use, baseURL: 'http://127.0.0.1:15174' },
  webServer: [{ command: 'npm run e2e:serve', url: 'http://127.0.0.1:15174',
    env: { E2E_API_PORT: '18081', E2E_WEB_PORT: '15174' }, reuseExistingServer: false, timeout: 180_000 }],
})
