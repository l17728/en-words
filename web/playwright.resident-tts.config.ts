import { defineConfig } from '@playwright/test'
import { resolve } from 'node:path'
import isolated from './playwright.closure.config'

export default defineConfig({
  ...isolated,
  testMatch: 'resident-tts-isolation.spec.ts',
  timeout: 90_000,
  use: { ...isolated.use, baseURL: 'http://127.0.0.1:15179', launchOptions: { args: ['--autoplay-policy=no-user-gesture-required'] } },
  webServer: [
    { command: 'node ../server/tools/zh_tts_resident_http.js', url: 'http://127.0.0.1:18002/v1/models',
      env: { ZH_TTS_ISOLATED_PORT: '18002', ZH_TTS_ISOLATED_WORKER: resolve('../.test-artifacts/zh-tts-resident/zh_tts_fake_worker.exe'),
        ZH_TTS_WORKERS: '1', PATH: `D:\\en-words-toolchain\\winlibs-gcc-16.2.0\\toolchain\\mingw64\\bin;${process.env.PATH || ''}` },
      reuseExistingServer: false, timeout: 30000 },
    { command: `${process.env.E2E_SERVER_BINARY ? `"${process.env.E2E_SERVER_BINARY}"` : 'go run ./cmd/server'} -testmode -port 18089 -db "${process.cwd()}/../.test-artifacts/resident-tts-browser.db"`,
      cwd: '../server', url: 'http://127.0.0.1:18089/api/v1/bootstrap/status', reuseExistingServer: false, timeout: 120_000 },
    { command: 'npm run e2e:serve', url: 'http://127.0.0.1:15179',
      env: { E2E_API_PORT: '18089', E2E_WEB_PORT: '15179' }, reuseExistingServer: false, timeout: 180_000 },
  ],
})
