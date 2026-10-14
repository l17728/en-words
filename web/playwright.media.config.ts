import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'
if (!process.env.E2E_SERVER_BINARY) throw new Error('Real media acceptance requires a reviewed server binary')
const backend = (isolated.webServer as any[])[0]
export default defineConfig({
  ...isolated, testMatch: 'real-media.spec.ts', timeout: 180_000,
  use: { ...isolated.use, baseURL: 'http://127.0.0.1:15175',
    launchOptions: { args: ['--autoplay-policy=no-user-gesture-required'] } },
  webServer: [
    { ...backend, command: backend.command.replace('-port 18080', '-port 18082') + ' -providers local', stdout: 'pipe',
      url: 'http://127.0.0.1:18082/api/v1/bootstrap/status',
      env: { ENWORDS_MODEL_CONSOLE: 'http://127.0.0.1:8090' } },
    { command: 'npm run e2e:serve', url: 'http://127.0.0.1:15175',
      env: { E2E_API_PORT: '18082', E2E_WEB_PORT: '15175' }, reuseExistingServer: false, timeout: 180_000 },
  ],
})
