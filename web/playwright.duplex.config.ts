import { defineConfig } from '@playwright/test'
import isolated from './playwright.recovery.config'
export default defineConfig<{ duplexPilot: boolean; duplexShadow: boolean }>({
  ...isolated,
  testMatch: 'duplex-*.spec.ts',
  timeout: 60_000,
  use: { ...isolated.use, duplexPilot: true },
  // recovery.config builds the frontend on every invocation. The duplex E2E
  // uses the built bundle and owns its isolated API process in the fixture.
  webServer: [{
    command: 'node node_modules/vite/bin/vite.js preview --config e2e/vite.closure.config.ts --host 127.0.0.1 --strictPort',
    url: 'http://127.0.0.1:15174',
    env: { E2E_API_PORT: '18081', E2E_WEB_PORT: '15174' },
    reuseExistingServer: false,
    timeout: 30_000,
  }],
})
