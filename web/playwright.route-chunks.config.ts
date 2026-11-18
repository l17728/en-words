import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'

const webPort = Number(process.env.E2E_WEB_PORT || 15173)

export default defineConfig({
  ...isolated,
  testMatch: ['route-chunking.spec.ts'],
  timeout: 60_000,
  webServer: [{
    command: `npm run build && npm run preview -- --host 127.0.0.1 --port ${webPort} --strictPort`,
    url: `http://127.0.0.1:${webPort}`,
    reuseExistingServer: false,
    timeout: 180_000,
  }],
})
